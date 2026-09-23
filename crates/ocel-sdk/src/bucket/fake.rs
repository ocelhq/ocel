use crate::proto::app::bucket::v1::{
    AbortMultipartRequest, AbortMultipartResponse, CompleteMultipartRequest,
    CompleteMultipartResponse, CopyRequest, CopyResponse, CreateMultipartRequest,
    CreateMultipartResponse, DeleteRequest, DeleteResponse, HeadRequest, HeadResponse, ListRequest,
    ListResponse, ObjectInfo, PresignedTarget, SignPartsRequest, SignPartsResponse, SignRequest,
    SignResponse, SignedOperation, SignedPart,
};
use buffa::Message;
use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

pub(super) const TOKEN: &str = "opensesame";
pub(super) const UPLOADED_AT: i64 = 1_700_000_000;

#[derive(Clone, Default)]
pub(super) struct Stored {
    pub(super) body: Vec<u8>,
    pub(super) content_type: String,
    pub(super) cache_control: String,
    pub(super) etag: String,
    pub(super) metadata: BTreeMap<String, String>,
}

#[derive(Default)]
pub(super) struct Store {
    objects: Mutex<BTreeMap<String, Stored>>,
    uploads: Mutex<BTreeMap<String, BTreeMap<i32, Vec<u8>>>>,
    pub(super) aborted: Mutex<Vec<String>>,
    pub(super) refuse_parts: AtomicBool,
    pub(super) refuse_complete: AtomicBool,
    versions: AtomicUsize,
    parts_at_once: AtomicUsize,
    pub(super) most_parts_at_once: AtomicUsize,
}

impl Store {
    pub(super) fn held(&self, key: &str) -> Option<Stored> {
        self.objects.lock().expect("the objects").get(key).cloned()
    }

    pub(super) fn hold(&self, key: &str, stored: Stored) {
        self.objects
            .lock()
            .expect("the objects")
            .insert(key.to_string(), stored);
    }

    fn tag(&self) -> String {
        format!("\"v{}\"", self.versions.fetch_add(1, Ordering::SeqCst))
    }
}

pub(super) struct Fake {
    pub(super) address: String,
    pub(super) store: Arc<Store>,
}

pub(super) fn stand() -> Fake {
    let store = Arc::new(Store::default());
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let address = format!("http://{}", listener.local_addr().expect("addr"));
    let served = store.clone();
    let base = address.clone();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { return };
            let (store, base) = (served.clone(), base.clone());
            std::thread::spawn(move || serve(stream, &store, &base));
        }
    });
    Fake { address, store }
}

struct Received {
    method: String,
    path: String,
    query: String,
    headers: BTreeMap<String, String>,
    body: Vec<u8>,
}

fn serve(mut stream: TcpStream, store: &Store, base: &str) {
    let Some(received) = read(&stream) else {
        return;
    };
    if received.headers.get("authorization").map(String::as_str) != Some(&format!("Bearer {TOKEN}"))
        && received.path.starts_with("/app.bucket.v1.")
    {
        answer(&mut stream, 403, "text/plain", Vec::new(), &[]);
        return;
    }
    if let Some(key) = received.path.strip_prefix("/o/") {
        let key = key.to_string();
        object(&mut stream, store, &key, &received);
        return;
    }
    let json = received
        .headers
        .get("content-type")
        .is_some_and(|value| value.ends_with("json"));
    match rpc(store, base, &received, json) {
        Some(body) => answer(
            &mut stream,
            200,
            received
                .headers
                .get("content-type")
                .map(String::as_str)
                .unwrap_or("application/proto"),
            body,
            &[],
        ),
        None => answer(&mut stream, 404, "text/plain", Vec::new(), &[]),
    }
}

fn read(stream: &TcpStream) -> Option<Received> {
    let mut reader = BufReader::new(stream.try_clone().ok()?);
    let mut line = String::new();
    reader.read_line(&mut line).ok()?;
    let mut words = line.split_whitespace();
    let method = words.next()?.to_string();
    let target = words.next()?.to_string();
    let (path, query) = match target.split_once('?') {
        Some((path, query)) => (path.to_string(), query.to_string()),
        None => (target, String::new()),
    };

    let mut headers = BTreeMap::new();
    loop {
        let mut header = String::new();
        reader.read_line(&mut header).ok()?;
        if header.trim().is_empty() {
            break;
        }
        let (name, value) = header.split_once(':')?;
        headers.insert(name.trim().to_ascii_lowercase(), value.trim().to_string());
    }
    let length: usize = headers
        .get("content-length")
        .and_then(|value| value.parse().ok())
        .unwrap_or_default();
    let mut body = vec![0u8; length];
    reader.read_exact(&mut body).ok()?;
    Some(Received {
        method,
        path,
        query,
        headers,
        body,
    })
}

fn answer(
    stream: &mut TcpStream,
    status: u16,
    content_type: &str,
    body: Vec<u8>,
    extra: &[(&str, String)],
) {
    let mut head = format!(
        "HTTP/1.1 {status} OK\r\ncontent-type: {content_type}\r\ncontent-length: {}\r\nconnection: close\r\n",
        body.len()
    );
    for (name, value) in extra {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(&body);
    let _ = stream.flush();
}

fn object(stream: &mut TcpStream, store: &Store, key: &str, received: &Received) {
    let query: BTreeMap<&str, &str> = received
        .query
        .split('&')
        .filter_map(|pair| pair.split_once('='))
        .collect();

    if received.method == "GET" {
        let Some(held) = store.held(key) else {
            answer(stream, 404, "text/plain", Vec::new(), &[]);
            return;
        };
        let (status, body) = match received.headers.get("range") {
            Some(range) => (206, sliced(&held.body, range)),
            None => (200, held.body.clone()),
        };
        answer(stream, status, &held.content_type, body, &[]);
        return;
    }

    if let (Some(upload), Some(part)) = (query.get("upload"), query.get("part")) {
        let at_once = store.parts_at_once.fetch_add(1, Ordering::SeqCst) + 1;
        store
            .most_parts_at_once
            .fetch_max(at_once, Ordering::SeqCst);
        std::thread::sleep(std::time::Duration::from_millis(20));
        store.parts_at_once.fetch_sub(1, Ordering::SeqCst);
        if store.refuse_parts.load(Ordering::SeqCst) {
            answer(stream, 500, "text/plain", Vec::new(), &[]);
            return;
        }
        let number: i32 = part.parse().unwrap_or_default();
        store
            .uploads
            .lock()
            .expect("the uploads")
            .entry((*upload).to_string())
            .or_default()
            .insert(number, received.body.clone());
        answer(
            stream,
            200,
            "text/plain",
            Vec::new(),
            &[("etag", format!("\"part-{number}\""))],
        );
        return;
    }

    let held = store.held(key);
    if received.headers.get("if-none-match").map(String::as_str) == Some("*") && held.is_some() {
        answer(stream, 412, "text/plain", Vec::new(), &[]);
        return;
    }
    if let Some(wanted) = received.headers.get("if-match") {
        if held.as_ref().map(|held| held.etag.as_str()) != Some(wanted.as_str()) {
            answer(stream, 412, "text/plain", Vec::new(), &[]);
            return;
        }
    }
    let etag = store.tag();
    store.hold(
        key,
        Stored {
            body: received.body.clone(),
            content_type: received
                .headers
                .get("content-type")
                .cloned()
                .unwrap_or_default(),
            cache_control: received
                .headers
                .get("cache-control")
                .cloned()
                .unwrap_or_default(),
            etag: etag.clone(),
            metadata: received
                .headers
                .iter()
                .filter_map(|(name, value)| {
                    Some((name.strip_prefix("x-amz-meta-")?.to_string(), value.clone()))
                })
                .collect(),
        },
    );
    answer(stream, 200, "text/plain", Vec::new(), &[("etag", etag)]);
}

fn sliced(body: &[u8], range: &str) -> Vec<u8> {
    let Some(span) = range.strip_prefix("bytes=") else {
        return body.to_vec();
    };
    let (from, to) = span.split_once('-').unwrap_or((span, ""));
    let from: usize = from.parse().unwrap_or_default();
    let to: usize = to.parse().unwrap_or(body.len().saturating_sub(1));
    body[from.min(body.len())..(to + 1).min(body.len())].to_vec()
}

fn info(key: &str, held: &Stored) -> ObjectInfo {
    ObjectInfo {
        key: key.to_string(),
        size: held.body.len() as i64,
        etag: held.etag.clone(),
        content_type: held.content_type.clone(),
        uploaded_at: buffa_types::google::protobuf::Timestamp {
            seconds: UPLOADED_AT,
            ..Default::default()
        }
        .into(),
        metadata: held
            .metadata
            .iter()
            .map(|(name, value)| (name.clone(), value.clone()))
            .collect(),
        ..Default::default()
    }
}

fn decode<T>(body: &[u8], json: bool) -> T
where
    T: Message + serde::de::DeserializeOwned,
{
    if json {
        return serde_json::from_slice(body).expect("a request in json");
    }
    buffa::DecodeOptions::new()
        .decode_from_slice(body)
        .expect("a request in binary proto")
}

fn encode<T>(message: &T, json: bool) -> Vec<u8>
where
    T: Message + serde::Serialize,
{
    if json {
        return serde_json::to_vec(message).expect("a response in json");
    }
    message.encode_to_vec()
}

fn rpc(store: &Store, base: &str, received: &Received, json: bool) -> Option<Vec<u8>> {
    let method = received.path.rsplit_once('/')?.1;
    let body = received.body.as_slice();
    Some(match method {
        "Head" => {
            let request: HeadRequest = decode(body, json);
            encode(
                &HeadResponse {
                    object: store
                        .held(&request.key)
                        .map(|held| info(&request.key, &held))
                        .into(),
                    ..Default::default()
                },
                json,
            )
        }
        "List" => {
            let request: ListRequest = decode(body, json);
            let objects = store.objects.lock().expect("the objects");
            let matching: Vec<ObjectInfo> = objects
                .iter()
                .filter(|(key, _)| key.starts_with(&request.prefix))
                .map(|(key, held)| info(key, held))
                .collect();
            let from: usize = request.cursor.parse().unwrap_or_default();
            let page = match request.limit {
                0 => matching.len(),
                limit => limit as usize,
            };
            let to = (from + page).min(matching.len());
            encode(
                &ListResponse {
                    objects: matching[from..to].to_vec(),
                    next_cursor: match to < matching.len() {
                        true => to.to_string(),
                        false => String::new(),
                    },
                    ..Default::default()
                },
                json,
            )
        }
        "Delete" => {
            let request: DeleteRequest = decode(body, json);
            let mut objects = store.objects.lock().expect("the objects");
            for key in &request.keys {
                objects.remove(key);
            }
            encode(&DeleteResponse::default(), json)
        }
        "Copy" => {
            let request: CopyRequest = decode(body, json);
            let held = store.held(&request.source_key)?;
            store.hold(&request.destination_key, held.clone());
            encode(
                &CopyResponse {
                    object: info(&request.destination_key, &held).into(),
                    ..Default::default()
                },
                json,
            )
        }
        "Sign" => {
            let request: SignRequest = decode(body, json);
            encode(
                &SignResponse {
                    target: PresignedTarget {
                        url: format!("{base}/o/{}", request.key),
                        key: request.key.clone(),
                        method: match request.operation.as_known() {
                            Some(SignedOperation::Get) => "GET",
                            Some(SignedOperation::PostUpload) => "POST",
                            _ => "PUT",
                        }
                        .to_string(),
                        ..Default::default()
                    }
                    .into(),
                    ..Default::default()
                },
                json,
            )
        }
        "CreateMultipart" => {
            let request: CreateMultipartRequest = decode(body, json);
            let id = format!("upload-for-{}", request.key);
            store
                .uploads
                .lock()
                .expect("the uploads")
                .insert(id.clone(), BTreeMap::new());
            encode(
                &CreateMultipartResponse {
                    upload_id: id,
                    ..Default::default()
                },
                json,
            )
        }
        "SignParts" => {
            let request: SignPartsRequest = decode(body, json);
            encode(
                &SignPartsResponse {
                    parts: request
                        .part_numbers
                        .iter()
                        .map(|number| SignedPart {
                            part_number: *number,
                            url: format!(
                                "{base}/o/{}?upload={}&part={number}",
                                request.key, request.upload_id
                            ),
                            ..Default::default()
                        })
                        .collect(),
                    ..Default::default()
                },
                json,
            )
        }
        "CompleteMultipart" => {
            if store.refuse_complete.load(Ordering::SeqCst) {
                return None;
            }
            let request: CompleteMultipartRequest = decode(body, json);
            let parts = store
                .uploads
                .lock()
                .expect("the uploads")
                .remove(&request.upload_id)?;
            let mut whole = Vec::new();
            for number in request.parts.iter().map(|part| part.part_number) {
                whole.extend_from_slice(parts.get(&number)?);
            }
            let held = Stored {
                body: whole,
                etag: store.tag(),
                ..Default::default()
            };
            store.hold(&request.key, held.clone());
            encode(
                &CompleteMultipartResponse {
                    object: info(&request.key, &held).into(),
                    ..Default::default()
                },
                json,
            )
        }
        "AbortMultipart" => {
            let request: AbortMultipartRequest = decode(body, json);
            store
                .uploads
                .lock()
                .expect("the uploads")
                .remove(&request.upload_id);
            store
                .aborted
                .lock()
                .expect("the aborts")
                .push(request.upload_id);
            encode(&AbortMultipartResponse::default(), json)
        }
        _ => return None,
    })
}
