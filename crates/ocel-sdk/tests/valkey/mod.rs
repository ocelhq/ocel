#![allow(dead_code)]

use std::collections::{BTreeSet, HashMap, VecDeque};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    Text(String),
    List(VecDeque<String>),
    Set(BTreeSet<String>),
}

struct Stored {
    value: Value,
    expires: Option<Instant>,
}

enum Reply {
    Simple(&'static str),
    Error(String),
    Integer(i64),
    Bulk(Option<String>),
    Array(Vec<Reply>),
}

#[derive(Clone)]
pub struct Valkey {
    pub port: u16,
    keys: Arc<Mutex<HashMap<String, Stored>>>,
}

impl Valkey {
    pub fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("a loopback port");
        let server = Self {
            port: listener.local_addr().expect("the bound address").port(),
            keys: Arc::default(),
        };
        let keys = server.keys.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming().flatten() {
                let keys = keys.clone();
                std::thread::spawn(move || serve(stream, keys));
            }
        });
        server
    }

    pub fn binding(&self, name: &str) -> String {
        format!(
            r#"{{"name":"kv--{name}","kv":{{"host":"127.0.0.1","port":{},"username":"app","password":"s3cret"}}}}"#,
            self.port
        )
    }

    pub fn get(&self, key: &str) -> Option<Value> {
        let mut keys = self.keys.lock().unwrap();
        live(&mut keys, key).map(|stored| stored.value.clone())
    }

    pub fn text(&self, key: &str) -> Option<String> {
        match self.get(key) {
            Some(Value::Text(text)) => Some(text),
            _ => None,
        }
    }

    pub fn set(&self, key: &str, value: Value) {
        self.keys.lock().unwrap().insert(
            key.to_string(),
            Stored {
                value,
                expires: None,
            },
        );
    }

    pub fn set_text(&self, key: &str, text: &str) {
        self.set(key, Value::Text(text.to_string()));
    }

    pub fn expire(&self, key: &str, after: Duration) {
        if let Some(stored) = self.keys.lock().unwrap().get_mut(key) {
            stored.expires = Some(Instant::now() + after);
        }
    }

    pub fn keys(&self) -> Vec<String> {
        let mut keys: Vec<String> = self.keys.lock().unwrap().keys().cloned().collect();
        keys.sort();
        keys
    }

    pub fn seconds_left(&self, key: &str) -> Option<u64> {
        let keys = self.keys.lock().unwrap();
        let expires = keys.get(key)?.expires?;
        let left = expires.saturating_duration_since(Instant::now());
        Some(left.as_millis().div_ceil(1000) as u64)
    }
}

fn live<'a>(keys: &'a mut HashMap<String, Stored>, key: &str) -> Option<&'a mut Stored> {
    if keys
        .get(key)
        .and_then(|stored| stored.expires)
        .is_some_and(|expires| expires <= Instant::now())
    {
        keys.remove(key);
    }
    keys.get_mut(key)
}

fn serve(stream: TcpStream, keys: Arc<Mutex<HashMap<String, Stored>>>) {
    let mut writer = stream.try_clone().expect("a writable stream");
    let mut reader = BufReader::new(stream);
    let mut queued: Option<Vec<Vec<String>>> = None;
    while let Some(command) = read_command(&mut reader) {
        let name = command[0].to_ascii_uppercase();
        let reply = match (name.as_str(), &mut queued) {
            ("MULTI", _) => {
                queued = Some(Vec::new());
                Reply::Simple("OK")
            }
            ("EXEC", Some(_)) => {
                let commands = queued.take().unwrap_or_default();
                let mut keys = keys.lock().unwrap();
                Reply::Array(
                    commands
                        .iter()
                        .map(|command| run(&mut keys, command))
                        .collect(),
                )
            }
            (_, Some(commands)) => {
                commands.push(command);
                Reply::Simple("QUEUED")
            }
            _ => run(&mut keys.lock().unwrap(), &command),
        };
        let mut encoded = Vec::new();
        encode(&reply, &mut encoded);
        if writer.write_all(&encoded).is_err() {
            return;
        }
    }
}

fn read_command(reader: &mut BufReader<TcpStream>) -> Option<Vec<String>> {
    let header = read_line(reader)?;
    let count: usize = header.strip_prefix('*')?.parse().ok()?;
    let mut parts = Vec::with_capacity(count);
    for _ in 0..count {
        let length: usize = read_line(reader)?.strip_prefix('$')?.parse().ok()?;
        let mut data = vec![0; length + 2];
        reader.read_exact(&mut data).ok()?;
        data.truncate(length);
        parts.push(String::from_utf8(data).ok()?);
    }
    Some(parts)
}

fn read_line(reader: &mut BufReader<TcpStream>) -> Option<String> {
    let mut line = String::new();
    if reader.read_line(&mut line).ok()? == 0 {
        return None;
    }
    Some(line.trim_end().to_string())
}

fn encode(reply: &Reply, out: &mut Vec<u8>) {
    match reply {
        Reply::Simple(text) => out.extend_from_slice(format!("+{text}\r\n").as_bytes()),
        Reply::Error(text) => out.extend_from_slice(format!("-{text}\r\n").as_bytes()),
        Reply::Integer(n) => out.extend_from_slice(format!(":{n}\r\n").as_bytes()),
        Reply::Bulk(None) => out.extend_from_slice(b"$-1\r\n"),
        Reply::Bulk(Some(text)) => {
            out.extend_from_slice(format!("${}\r\n{text}\r\n", text.len()).as_bytes())
        }
        Reply::Array(items) => {
            out.extend_from_slice(format!("*{}\r\n", items.len()).as_bytes());
            for item in items {
                encode(item, out);
            }
        }
    }
}

fn wrong_type() -> Reply {
    Reply::Error("WRONGTYPE Operation against a key holding the wrong kind of value".into())
}

fn run(keys: &mut HashMap<String, Stored>, command: &[String]) -> Reply {
    let name = command[0].to_ascii_uppercase();
    let arg = |i: usize| command.get(i).cloned().unwrap_or_default();
    let int = |i: usize| arg(i).parse::<i64>().unwrap_or_default();
    match name.as_str() {
        "PING" => Reply::Simple("PONG"),
        "AUTH" | "SELECT" | "CLIENT" => Reply::Simple("OK"),
        "GET" => match live(keys, &arg(1)).map(|stored| &stored.value) {
            None => Reply::Bulk(None),
            Some(Value::Text(text)) => Reply::Bulk(Some(text.clone())),
            Some(_) => wrong_type(),
        },
        "SET" => {
            let key = arg(1);
            let options: Vec<String> = command[3..]
                .iter()
                .map(|o| o.to_ascii_uppercase())
                .collect();
            let kept = live(keys, &key).and_then(|stored| stored.expires);
            let expires = if let Some(at) = options.iter().position(|o| o == "PX") {
                Some(Instant::now() + Duration::from_millis(int(3 + at + 1) as u64))
            } else if options.iter().any(|o| o == "KEEPTTL") {
                kept
            } else {
                None
            };
            keys.insert(
                key,
                Stored {
                    value: Value::Text(arg(2)),
                    expires,
                },
            );
            Reply::Simple("OK")
        }
        "INCRBY" | "DECRBY" => {
            let step = if name == "INCRBY" { int(2) } else { -int(2) };
            let key = arg(1);
            let current = match live(keys, &key).map(|stored| &stored.value) {
                None => 0,
                Some(Value::Text(text)) => match text.parse::<i64>() {
                    Ok(n) => n,
                    Err(_) => return Reply::Error("ERR value is not an integer".into()),
                },
                Some(_) => return wrong_type(),
            };
            let next = current + step;
            let expires = keys.get(&key).and_then(|stored| stored.expires);
            keys.insert(
                key,
                Stored {
                    value: Value::Text(next.to_string()),
                    expires,
                },
            );
            Reply::Integer(next)
        }
        "PEXPIRE" => match live(keys, &arg(1)) {
            Some(stored) => {
                stored.expires = Some(Instant::now() + Duration::from_millis(int(2) as u64));
                Reply::Integer(1)
            }
            None => Reply::Integer(0),
        },
        "PERSIST" => match live(keys, &arg(1)) {
            Some(stored) if stored.expires.is_some() => {
                stored.expires = None;
                Reply::Integer(1)
            }
            _ => Reply::Integer(0),
        },
        "DEL" => Reply::Integer(
            command[1..]
                .iter()
                .filter(|key| live(keys, key).is_some() && keys.remove(key.as_str()).is_some())
                .count() as i64,
        ),
        "RPUSH" | "LPUSH" => {
            let key = arg(1);
            let stored = keys.entry(key).or_insert(Stored {
                value: Value::List(VecDeque::new()),
                expires: None,
            });
            let Value::List(list) = &mut stored.value else {
                return wrong_type();
            };
            for value in &command[2..] {
                if name == "RPUSH" {
                    list.push_back(value.clone());
                } else {
                    list.push_front(value.clone());
                }
            }
            Reply::Integer(list.len() as i64)
        }
        "RPOP" | "LPOP" => {
            let key = arg(1);
            let popped = match live(keys, &key).map(|stored| &mut stored.value) {
                None => None,
                Some(Value::List(list)) => {
                    if name == "RPOP" {
                        list.pop_back()
                    } else {
                        list.pop_front()
                    }
                }
                Some(_) => return wrong_type(),
            };
            if matches!(keys.get(&key).map(|s| &s.value), Some(Value::List(list)) if list.is_empty())
            {
                keys.remove(&key);
            }
            Reply::Bulk(popped)
        }
        "LINDEX" | "LRANGE" | "LLEN" => {
            let list = match live(keys, &arg(1)).map(|stored| &stored.value) {
                None => VecDeque::new(),
                Some(Value::List(list)) => list.clone(),
                Some(_) => return wrong_type(),
            };
            let length = list.len() as i64;
            let at = |index: i64| if index < 0 { length + index } else { index };
            match name.as_str() {
                "LLEN" => Reply::Integer(length),
                "LINDEX" => {
                    let index = at(int(2));
                    Reply::Bulk(
                        (0..length)
                            .contains(&index)
                            .then(|| list[index as usize].clone()),
                    )
                }
                _ => {
                    let start = at(int(2)).max(0);
                    let stop = at(int(3)).min(length - 1);
                    Reply::Array(
                        (start..=stop)
                            .map(|i| Reply::Bulk(Some(list[i as usize].clone())))
                            .collect(),
                    )
                }
            }
        }
        "SADD" => {
            let stored = keys.entry(arg(1)).or_insert(Stored {
                value: Value::Set(BTreeSet::new()),
                expires: None,
            });
            let Value::Set(set) = &mut stored.value else {
                return wrong_type();
            };
            Reply::Integer(
                command[2..]
                    .iter()
                    .filter(|member| set.insert((*member).clone()))
                    .count() as i64,
            )
        }
        "SREM" | "SISMEMBER" | "SCARD" | "SMEMBERS" => {
            let set = match live(keys, &arg(1)).map(|stored| &mut stored.value) {
                None => &mut BTreeSet::new(),
                Some(Value::Set(set)) => set,
                Some(_) => return wrong_type(),
            };
            match name.as_str() {
                "SREM" => Reply::Integer(
                    command[2..]
                        .iter()
                        .filter(|member| set.remove(member.as_str()))
                        .count() as i64,
                ),
                "SISMEMBER" => Reply::Integer(set.contains(&arg(2)) as i64),
                "SCARD" => Reply::Integer(set.len() as i64),
                _ => Reply::Array(
                    set.iter()
                        .map(|member| Reply::Bulk(Some(member.clone())))
                        .collect(),
                ),
            }
        }
        other => Reply::Error(format!("ERR unknown command '{other}'")),
    }
}
