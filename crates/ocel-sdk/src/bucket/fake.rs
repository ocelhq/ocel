use crate::proto::app::bucket::v1::{
    AbortMultipartRequest, AbortMultipartResponse, CompleteMultipartRequest,
    CompleteMultipartResponse, CopyRequest, CopyResponse, CreateMultipartRequest,
    CreateMultipartResponse, DeleteRequest, DeleteResponse, HeadRequest, HeadResponse, ListRequest,
    ListResponse, ObjectInfo, PresignedTarget, SignConstraints, SignPartsRequest,
    SignPartsResponse, SignRequest, SignResponse, SignedOperation, SignedPart,
};
use buffa::Message;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

pub(super) const TOKEN: &str = "opensesame";
pub(super) const UPLOADED_AT: i64 = 1_700_000_000;
const METADATA_HEADER: &str = "x-fake-meta-";
const ETAG_HEADER: &str = "X-Fake-Version";
const TRANSPORT_HEADERS: [&str; 6] = [
    "host",
    "content-length",
    "connection",
    "user-agent",
    "accept-encoding",
    "transfer-encoding",
];

#[derive(Clone, Default)]
pub(super) struct Stored {
    pub(super) body: Vec<u8>,
    pub(super) content_type: String,
    pub(super) cache_control: String,
    pub(super) etag: String,
    pub(super) metadata: BTreeMap<String, String>,
}

#[derive(Default)]
struct Pending {
    described: Stored,
    parts: BTreeMap<i32, Vec<u8>>,
}

#[derive(Default)]
pub(super) struct Store {
    objects: Mutex<BTreeMap<String, Stored>>,
    uploads: Mutex<BTreeMap<String, Pending>>,
    signed_writes: Mutex<Vec<SignConstraints>>,
    pub(super) aborted: Mutex<Vec<String>>,
    pub(super) refuse_parts: AtomicBool,
    pub(super) refuse_complete: AtomicBool,
    pub(super) withhold_etag: AtomicBool,
    pub(super) rival_lands_after_write: AtomicBool,
    replace_before_reads: Mutex<usize>,
    pub(super) heads: AtomicUsize,
    written: Mutex<Vec<(String, String)>>,
    versions: AtomicUsize,
    parts_at_once: AtomicUsize,
    pub(super) most_parts_at_once: AtomicUsize,
}

impl Store {
    pub(super) fn object(&self, key: &str) -> Option<Stored> {
        self.objects.lock().expect("the objects").get(key).cloned()
    }

    pub(super) fn insert(&self, key: &str, stored: Stored) {
        self.objects
            .lock()
            .expect("the objects")
            .insert(key.to_string(), stored);
    }

    pub(super) fn etags_written(&self, key: &str) -> Vec<String> {
        self.written
            .lock()
            .expect("the writes")
            .iter()
            .filter(|(written, _)| written == key)
            .map(|(_, etag)| etag.clone())
            .collect()
    }

    fn record_signed_write(&self, constraints: SignConstraints) -> usize {
        let mut signed = self.signed_writes.lock().expect("the signed writes");
        signed.push(constraints);
        signed.len() - 1
    }

    fn find_signed_write(&self, signature: &str) -> Option<SignConstraints> {
        let index: usize = signature.parse().ok()?;
        self.signed_writes
            .lock()
            .expect("the signed writes")
            .get(index)
            .cloned()
    }

    fn tag(&self) -> String {
        format!("\"v{}\"", self.versions.fetch_add(1, Ordering::SeqCst))
    }

    fn write(&self, key: &str, stored: Stored) -> String {
        let etag = stored.etag.clone();
        self.written
            .lock()
            .expect("the writes")
            .push((key.to_string(), etag.clone()));
        self.insert(key, stored);
        if self.rival_lands_after_write.load(Ordering::SeqCst) {
            self.replace(key);
        }
        match self.withhold_etag.load(Ordering::SeqCst) {
            true => String::new(),
            false => etag,
        }
    }

    fn replace(&self, key: &str) {
        let rival = Stored {
            body: b"a rival's bytes".to_vec(),
            content_type: "text/plain".to_string(),
            etag: self.tag(),
            ..Default::default()
        };
        self.insert(key, rival);
    }

    pub(super) fn replace_before_reads(&self, reads: usize) {
        *self.replace_before_reads.lock().expect("the reads") = reads;
    }

    fn replaces_before_this_read(&self) -> bool {
        let mut left = self.replace_before_reads.lock().expect("the reads");
        let Some(fewer) = left.checked_sub(1) else {
            return false;
        };
        *left = fewer;
        true
    }
}

pub(super) struct Fake {
    pub(super) address: String,
    pub(super) store: Arc<Store>,
}

pub(super) fn start() -> Fake {
    let store = Arc::new(Store::default());
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let address = format!("http://{}", listener.local_addr().expect("addr"));
    accept(listener, store.clone(), address.clone(), None);
    Fake { address, store }
}

pub(super) fn start_with_https_objects() -> Fake {
    let store = Arc::new(Store::default());
    let rpc = TcpListener::bind("127.0.0.1:0").expect("bind");
    let objects = TcpListener::bind("127.0.0.1:0").expect("bind");
    let address = format!("http://{}", rpc.local_addr().expect("addr"));
    let base = format!("https://{}", objects.local_addr().expect("addr"));
    accept(rpc, store.clone(), base.clone(), None);
    accept(objects, store.clone(), base, Some(read_tls_config()));
    Fake { address, store }
}

pub(super) fn read_trusted_roots() -> rustls::RootCertStore {
    let mut roots = rustls::RootCertStore::empty();
    for certificate in CertificateDer::pem_slice_iter(include_bytes!("testdata/ca.pem")) {
        roots
            .add(certificate.expect("the test CA"))
            .expect("the test CA");
    }
    roots
}

fn read_tls_config() -> Arc<rustls::ServerConfig> {
    let chain = CertificateDer::pem_slice_iter(include_bytes!("testdata/store.pem"))
        .collect::<Result<Vec<_>, _>>()
        .expect("the test store certificate");
    let key = PrivateKeyDer::from_pem_slice(include_bytes!("testdata/store.key"))
        .expect("the test store key");
    Arc::new(
        rustls::ServerConfig::builder_with_provider(Arc::new(
            rustls::crypto::ring::default_provider(),
        ))
        .with_safe_default_protocol_versions()
        .expect("the protocol versions")
        .with_no_client_auth()
        .with_single_cert(chain, key)
        .expect("the test store config"),
    )
}

fn accept(
    listener: TcpListener,
    store: Arc<Store>,
    base: String,
    tls: Option<Arc<rustls::ServerConfig>>,
) {
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { return };
            let (store, base, tls) = (store.clone(), base.clone(), tls.clone());
            std::thread::spawn(move || match tls {
                Some(tls) => {
                    let Ok(connection) = rustls::ServerConnection::new(tls) else {
                        return;
                    };
                    serve(rustls::StreamOwned::new(connection, stream), &store, &base)
                }
                None => serve(stream, &store, &base),
            });
        }
    });
}

struct Received {
    method: String,
    path: String,
    query: String,
    headers: BTreeMap<String, String>,
    body: Vec<u8>,
}

fn serve(stream: impl Read + Write, store: &Store, base: &str) {
    let mut reader = BufReader::new(stream);
    let Some(received) = read(&mut reader) else {
        return;
    };
    let stream = reader.get_mut();
    if received.headers.get("authorization").map(String::as_str) != Some(&format!("Bearer {TOKEN}"))
        && received.path.starts_with("/app.bucket.v1.")
    {
        answer(stream, 403, "text/plain", Vec::new(), &[]);
        return;
    }
    if let Some(key) = received.path.strip_prefix("/o/") {
        let key = key.to_string();
        object(stream, store, &key, &received);
        return;
    }
    let json = received
        .headers
        .get("content-type")
        .is_some_and(|value| value.ends_with("json"));
    match rpc(store, base, &received, json) {
        Some(body) => answer(
            stream,
            200,
            received
                .headers
                .get("content-type")
                .map(String::as_str)
                .unwrap_or("application/proto"),
            body,
            &[],
        ),
        None => answer(stream, 404, "text/plain", Vec::new(), &[]),
    }
}

fn read(reader: &mut impl BufRead) -> Option<Received> {
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
        headers
            .entry(name.trim().to_ascii_lowercase())
            .and_modify(|combined: &mut String| {
                combined.push(',');
                combined.push_str(value.trim());
            })
            .or_insert_with(|| value.trim().to_string());
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
    stream: &mut impl Write,
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

fn object(stream: &mut impl Write, store: &Store, key: &str, received: &Received) {
    let query: BTreeMap<&str, &str> = received
        .query
        .split('&')
        .filter_map(|pair| pair.split_once('='))
        .collect();

    if received.method == "GET" {
        if store.replaces_before_this_read() {
            store.replace(key);
        }
        let Some(stored) = store.object(key) else {
            answer(stream, 404, "text/plain", Vec::new(), &[]);
            return;
        };
        let (status, body) = match received.headers.get("range") {
            Some(range) => (206, sliced(&stored.body, range)),
            None => (200, stored.body.clone()),
        };
        answer(
            stream,
            status,
            &stored.content_type,
            body,
            &[(ETAG_HEADER, stored.etag.clone())],
        );
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
            .parts
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

    let Some(constraints) = query
        .get("signature")
        .and_then(|signature| store.find_signed_write(signature))
    else {
        answer(stream, 403, "text/plain", Vec::new(), &[]);
        return;
    };
    let sent: BTreeMap<String, String> = received
        .headers
        .iter()
        .filter(|(name, _)| !TRANSPORT_HEADERS.contains(&name.as_str()))
        .map(|(name, value)| (name.clone(), value.clone()))
        .collect();
    if sent != signed_headers(&constraints) {
        answer(stream, 403, "text/plain", Vec::new(), &[]);
        return;
    }
    let existing = store.object(key);
    if constraints.if_none_match == "*" && existing.is_some() {
        answer(stream, 412, "text/plain", Vec::new(), &[]);
        return;
    }
    if !constraints.if_match.is_empty()
        && existing.as_ref().map(|existing| existing.etag.as_str())
            != Some(constraints.if_match.as_str())
    {
        answer(stream, 412, "text/plain", Vec::new(), &[]);
        return;
    }
    let etag = store.write(
        key,
        Stored {
            body: received.body.clone(),
            content_type: constraints.content_type.clone(),
            cache_control: constraints.cache_control.clone(),
            etag: store.tag(),
            metadata: constraints
                .metadata
                .iter()
                .map(|(name, value)| (name.clone(), value.clone()))
                .collect(),
        },
    );
    let answered = match etag.is_empty() {
        true => Vec::new(),
        false => vec![(ETAG_HEADER, etag)],
    };
    answer(stream, 200, "text/plain", Vec::new(), &answered);
}

fn signed_headers(constraints: &SignConstraints) -> BTreeMap<String, String> {
    let mut headers: BTreeMap<String, String> = [
        ("content-type", &constraints.content_type),
        ("cache-control", &constraints.cache_control),
        ("if-none-match", &constraints.if_none_match),
        ("if-match", &constraints.if_match),
    ]
    .into_iter()
    .filter(|(_, value)| !value.is_empty())
    .map(|(name, value)| (name.to_string(), value.clone()))
    .collect();
    for (name, value) in &constraints.metadata {
        headers.insert(format!("{METADATA_HEADER}{name}"), value.clone());
    }
    headers
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

fn info(key: &str, stored: &Stored) -> ObjectInfo {
    ObjectInfo {
        key: key.to_string(),
        size: stored.body.len() as i64,
        etag: stored.etag.clone(),
        content_type: stored.content_type.clone(),
        uploaded_at: buffa_types::google::protobuf::Timestamp {
            seconds: UPLOADED_AT,
            ..Default::default()
        }
        .into(),
        metadata: stored
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
            store.heads.fetch_add(1, Ordering::SeqCst);
            let request: HeadRequest = decode(body, json);
            encode(
                &HeadResponse {
                    object: store
                        .object(&request.key)
                        .map(|stored| info(&request.key, &stored))
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
                .map(|(key, stored)| info(key, stored))
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
            let source = store.object(&request.source_key)?;
            store.insert(&request.destination_key, source.clone());
            encode(
                &CopyResponse {
                    object: info(&request.destination_key, &source).into(),
                    ..Default::default()
                },
                json,
            )
        }
        "Sign" => {
            let request: SignRequest = decode(body, json);
            let operation = request.operation.as_known();
            let mut target = PresignedTarget {
                url: format!("{base}/o/{}", request.key),
                key: request.key.clone(),
                etag_header: ETAG_HEADER.to_string(),
                method: match operation {
                    Some(SignedOperation::Get) => "GET",
                    Some(SignedOperation::PostUpload) => "POST",
                    _ => "PUT",
                }
                .to_string(),
                ..Default::default()
            };
            if operation == Some(SignedOperation::Put) {
                let constraints = request.constraints.into_option().unwrap_or_default();
                target.headers = signed_headers(&constraints).into_iter().collect();
                let signature = store.record_signed_write(constraints);
                target.url = format!("{}?signature={signature}", target.url);
            }
            encode(
                &SignResponse {
                    target: target.into(),
                    ..Default::default()
                },
                json,
            )
        }
        "CreateMultipart" => {
            let request: CreateMultipartRequest = decode(body, json);
            let id = format!("upload-for-{}", request.key);
            store.uploads.lock().expect("the uploads").insert(
                id.clone(),
                Pending {
                    described: Stored {
                        content_type: request.content_type,
                        cache_control: request.cache_control,
                        metadata: request.metadata.into_iter().collect(),
                        ..Default::default()
                    },
                    parts: BTreeMap::new(),
                },
            );
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
            let pending = store
                .uploads
                .lock()
                .expect("the uploads")
                .remove(&request.upload_id)?;
            let mut whole = Vec::new();
            for number in request.parts.iter().map(|part| part.part_number) {
                whole.extend_from_slice(pending.parts.get(&number)?);
            }
            let etag = store.write(
                &request.key,
                Stored {
                    body: whole,
                    etag: store.tag(),
                    ..pending.described
                },
            );
            encode(
                &CompleteMultipartResponse {
                    etag,
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
