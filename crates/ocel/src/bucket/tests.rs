use super::fake::{stand, Fake, TOKEN, UPLOADED_AT};
use super::Bucket;
use crate::Error;
use futures_util::StreamExt;
use std::sync::atomic::Ordering;
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, UNIX_EPOCH};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

static ENV: Mutex<()> = Mutex::new(());

struct Standing {
    fake: Fake,
    bucket: Bucket,
    _env: MutexGuard<'static, ()>,
}

fn standing(public_base_url: &str) -> Standing {
    let env = ENV.lock().unwrap_or_else(|held| held.into_inner());
    let fake = stand();
    std::env::set_var(
        "OCEL_RESOURCE_BUCKET_uploads",
        format!(
            r#"{{"name":"uploads","bucket":{{"bucket":"store","publicBaseUrl":"{public_base_url}"}}}}"#
        ),
    );
    std::env::set_var("OCEL_RUNTIME_ADDRESS", &fake.address);
    std::env::set_var("OCEL_SESSION_TOKEN", TOKEN);
    Standing {
        bucket: Bucket::new("uploads"),
        fake,
        _env: env,
    }
}

#[tokio::test]
async fn an_object_comes_back_as_it_was_written() {
    let standing = standing("");
    let written = standing
        .bucket
        .put("avatars/one.png", "the bytes")
        .content_type("image/png")
        .cache_control("max-age=60")
        .metadata([("owner", "ada")])
        .await
        .expect("the write");

    assert_eq!(written.key, "avatars/one.png");
    assert_eq!(written.size, 9);
    assert_eq!(written.content_type, "image/png");
    assert_eq!(written.metadata["owner"], "ada");
    assert_eq!(
        written.uploaded_at,
        Some(UNIX_EPOCH + Duration::from_secs(UPLOADED_AT as u64))
    );
    assert_eq!(
        standing
            .fake
            .store
            .held("avatars/one.png")
            .expect("the stored object")
            .cache_control,
        "max-age=60"
    );

    let read = standing
        .bucket
        .get("avatars/one.png")
        .await
        .expect("the read");
    assert_eq!(read.info().etag, written.etag);
    assert_eq!(&read.bytes().await.expect("the bytes")[..], b"the bytes");
}

#[tokio::test]
async fn a_range_reads_only_the_bytes_it_covers() {
    let standing = standing("");
    standing
        .bucket
        .put("ranged.txt", "0123456789")
        .await
        .expect("the write");

    let read = standing
        .bucket
        .get("ranged.txt")
        .range(2..5)
        .await
        .expect("the read");
    assert_eq!(read.info().size, 10);
    assert_eq!(&read.bytes().await.expect("the bytes")[..], b"234");
}

#[tokio::test]
async fn a_read_of_a_key_the_bucket_does_not_hold_names_the_key() {
    let standing = standing("");
    let Err(err) = standing.bucket.get("absent.txt").await else {
        panic!("a read of a key nothing is held under answered");
    };
    assert_eq!(
        err.to_string(),
        "the bucket holds no object under 'absent.txt'"
    );
    assert!(matches!(err, Error::NotFound { .. }));
}

#[tokio::test]
async fn a_head_of_a_key_the_bucket_does_not_hold_answers_with_nothing() {
    let standing = standing("");
    assert!(standing
        .bucket
        .head("absent.txt")
        .await
        .expect("a head")
        .is_none());
    assert!(!standing
        .bucket
        .exists("absent.txt")
        .await
        .expect("an existence"));
}

#[tokio::test]
async fn a_write_conditioned_on_the_key_being_free_is_refused_when_it_is_not() {
    let standing = standing("");
    standing
        .bucket
        .put("once.txt", "first")
        .if_not_exists()
        .await
        .expect("the first write");

    let err = standing
        .bucket
        .put("once.txt", "second")
        .if_not_exists()
        .await
        .expect_err("the key is taken");
    assert_eq!(
        err.to_string(),
        "the object under 'once.txt' did not meet the condition this write carried"
    );
    assert!(matches!(err, Error::PreconditionFailed { .. }));

    let written = standing
        .bucket
        .put("once.txt", "third")
        .if_match(
            &standing
                .bucket
                .head("once.txt")
                .await
                .expect("a head")
                .expect("the object")
                .etag,
        )
        .await
        .expect("the conditioned write");
    assert_eq!(written.size, 5);

    let err = standing
        .bucket
        .put("once.txt", "fourth")
        .if_match("\"stale\"")
        .await
        .expect_err("the etag moved on");
    assert!(matches!(err, Error::PreconditionFailed { .. }));
}

#[tokio::test]
async fn a_listing_pages_through_the_store_without_the_caller_seeing_a_cursor() {
    let standing = standing("");
    for index in 0..5 {
        standing
            .bucket
            .put(&format!("photos/{index}.txt"), "x")
            .await
            .expect("the write");
    }
    standing
        .bucket
        .put("notes/one.txt", "x")
        .await
        .expect("the write");

    let walked: Vec<String> = standing
        .bucket
        .list()
        .prefix("photos/")
        .limit(2)
        .into_stream()
        .map(|held| held.expect("an object").key)
        .collect()
        .await;
    assert_eq!(
        walked,
        [
            "photos/0.txt",
            "photos/1.txt",
            "photos/2.txt",
            "photos/3.txt",
            "photos/4.txt"
        ]
    );
}

#[tokio::test]
async fn a_delete_of_a_key_the_bucket_does_not_hold_is_not_an_error() {
    let standing = standing("");
    standing
        .bucket
        .put("gone.txt", "x")
        .await
        .expect("the write");

    standing
        .bucket
        .delete_many(["gone.txt", "never-there.txt"])
        .await
        .expect("the delete");
    assert!(!standing.bucket.exists("gone.txt").await.expect("a head"));
    standing
        .bucket
        .delete("never-there.txt")
        .await
        .expect("the second delete");
}

#[tokio::test]
async fn a_copy_leaves_the_same_bytes_under_the_new_key() {
    let standing = standing("");
    standing
        .bucket
        .put("from.txt", "carried")
        .await
        .expect("the write");

    let copied = standing
        .bucket
        .copy("from.txt", "to.txt")
        .await
        .expect("the copy");
    assert_eq!(copied.key, "to.txt");
    assert_eq!(
        &standing
            .bucket
            .get("to.txt")
            .await
            .expect("the read")
            .bytes()
            .await
            .expect("the bytes")[..],
        b"carried"
    );
}

#[tokio::test]
async fn a_body_too_large_for_one_request_goes_up_in_parts() {
    let standing = standing("");
    let bucket = standing.bucket.clone().with_thresholds(8, 4);
    let body: Vec<u8> = (0..26u8).map(|index| b'a' + index).collect();

    let written = bucket
        .put("big.bin", body.clone())
        .await
        .expect("the write");
    assert_eq!(written.size, 26);
    assert_eq!(
        standing
            .fake
            .store
            .held("big.bin")
            .expect("the stored object")
            .body,
        body
    );
    assert_eq!(
        standing
            .fake
            .store
            .most_parts_at_once
            .load(Ordering::SeqCst),
        super::PARTS_IN_FLIGHT,
        "the parts did not go up four at a time"
    );
    assert!(standing
        .fake
        .store
        .aborted
        .lock()
        .expect("the aborts")
        .is_empty());
}

#[tokio::test]
async fn a_part_the_store_refuses_throws_the_whole_upload_away() {
    let standing = standing("");
    let bucket = standing.bucket.clone().with_thresholds(8, 4);
    standing
        .fake
        .store
        .refuse_parts
        .store(true, Ordering::SeqCst);

    let err = bucket
        .put("doomed.bin", vec![b'x'; 26])
        .await
        .expect_err("the store refuses every part");
    assert!(matches!(err, Error::Refused { .. }), "error = {err}");
    assert_eq!(
        standing
            .fake
            .store
            .aborted
            .lock()
            .expect("the aborts")
            .as_slice(),
        ["upload-for-doomed.bin"]
    );
    assert!(standing.fake.store.held("doomed.bin").is_none());
}

#[tokio::test]
async fn a_reader_and_a_writer_carry_the_bytes_a_stream_at_a_time() {
    let standing = standing("");
    let bucket = standing.bucket.clone().with_thresholds(8, 4);

    let mut writer = bucket.writer("streamed.bin").await.expect("a writer");
    for _ in 0..4 {
        writer.write_all(b"abcdefg").await.expect("the write");
    }
    writer.shutdown().await.expect("the shutdown");

    let mut reader = bucket.reader("streamed.bin").await.expect("a reader");
    let mut read = Vec::new();
    reader.read_to_end(&mut read).await.expect("the read");
    assert_eq!(read, b"abcdefg".repeat(4));
}

#[tokio::test]
async fn a_writer_dropped_mid_flight_throws_its_upload_away() {
    let standing = standing("");
    let bucket = standing.bucket.clone().with_thresholds(8, 4);

    let mut writer = bucket.writer("abandoned.bin").await.expect("a writer");
    for _ in 0..4 {
        writer.write_all(b"abcdefg").await.expect("the write");
    }
    writer.write_all(b"hijklmn").await.expect("the write");
    drop(writer);
    tokio::task::yield_now().await;
    tokio::time::sleep(Duration::from_millis(50)).await;

    assert_eq!(
        standing
            .fake
            .store
            .aborted
            .lock()
            .expect("the aborts")
            .as_slice(),
        ["upload-for-abandoned.bin"],
        "a writer dropped while it was still draining left its parts costing storage forever"
    );
}

#[tokio::test]
async fn a_signature_names_the_target_the_bytes_go_to_and_come_from() {
    let standing = standing("https://storage.shop.example/store");
    standing
        .bucket
        .put("shared.txt", "x")
        .await
        .expect("the write");

    let url = standing
        .bucket
        .signed_url("shared.txt")
        .expires_in(Duration::from_secs(60))
        .download("shared.txt")
        .await
        .expect("a signed url");
    assert_eq!(url, format!("{}/o/shared.txt", standing.fake.address));

    let upload = standing
        .bucket
        .signed_upload("shared.txt")
        .expires_in(Duration::from_secs(60))
        .max_size(1024)
        .content_type("text/plain")
        .await
        .expect("a signed upload");
    assert_eq!(upload.method, "POST");
    assert_eq!(
        upload.url,
        format!("{}/o/shared.txt", standing.fake.address)
    );

    assert_eq!(
        standing
            .bucket
            .public_url("shared.txt")
            .expect("a public url"),
        "https://storage.shop.example/store/shared.txt"
    );
    assert_eq!(
        standing
            .bucket
            .public_url("a b/c#d?e.png")
            .expect("a public url"),
        "https://storage.shop.example/store/a%20b/c%23d%3Fe.png"
    );
}

#[tokio::test]
async fn a_read_hands_back_the_bytes_as_they_arrive() {
    let standing = standing("");
    standing
        .bucket
        .put("streamed.txt", "arriving")
        .await
        .expect("the write");

    let read = standing.bucket.get("streamed.txt").await.expect("the read");
    let mut arrived = Vec::new();
    let mut chunks = Box::pin(read.into_stream());
    while let Some(chunk) = chunks.next().await {
        arrived.extend_from_slice(&chunk.expect("a chunk"));
    }
    assert_eq!(arrived, b"arriving");
}

#[tokio::test]
async fn a_bucket_reached_during_discovery_says_it_stands_on_nothing_yet() {
    let standing = standing("");
    std::env::set_var("OCEL_PHASE", "discovery");
    let reached = standing.bucket.head("anything.txt").await;
    std::env::remove_var("OCEL_PHASE");

    let err = reached.expect_err("discovery provisions nothing");
    assert_eq!(
        err.to_string(),
        "'bucket(\"uploads\")' cannot be used during discovery: tried to access 'head' before the resource was provisioned"
    );
    assert!(matches!(err, Error::Unprovisioned { .. }));
}

#[tokio::test]
async fn a_bucket_with_no_public_address_says_so_rather_than_guess_one() {
    let standing = standing("");
    let err = standing
        .bucket
        .public_url("shared.txt")
        .expect_err("no public address");
    assert!(matches!(err, Error::NotPublic { .. }), "error = {err}");
    assert!(err.to_string().contains("#[ocel(public)]"), "error = {err}");
}
