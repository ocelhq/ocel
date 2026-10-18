use super::fake::{read_trusted_roots, start, start_with_https_objects, Fake, TOKEN, UPLOADED_AT};
use super::Bucket;
use crate::Error;
use futures_util::StreamExt;
use std::sync::atomic::Ordering;
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, UNIX_EPOCH};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

static ENV: Mutex<()> = Mutex::new(());

struct Harness {
    fake: Fake,
    bucket: Bucket,
    _env: MutexGuard<'static, ()>,
}

fn harness(public_base_url: &str) -> Harness {
    harness_of(start(), public_base_url)
}

fn harness_of(fake: Fake, public_base_url: &str) -> Harness {
    let env = ENV.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    std::env::set_var(
        "OCEL_RESOURCE_BUCKET_uploads",
        format!(
            r#"{{"name":"uploads","bucket":{{"bucket":"store","publicBaseUrl":"{public_base_url}"}}}}"#
        ),
    );
    std::env::set_var("OCEL_RUNTIME_ADDRESS", &fake.address);
    std::env::set_var("OCEL_SESSION_TOKEN", TOKEN);
    Harness {
        bucket: Bucket::new("uploads"),
        fake,
        _env: env,
    }
}

#[tokio::test]
async fn an_object_comes_back_as_it_was_written() {
    let harness = harness("");
    let written = harness
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
    assert_eq!(written.uploaded_at, None);
    assert_eq!(
        harness
            .fake
            .store
            .object("avatars/one.png")
            .expect("the stored object")
            .cache_control,
        "max-age=60"
    );

    let read = harness
        .bucket
        .get("avatars/one.png")
        .await
        .expect("the read");
    assert_eq!(read.info().etag, written.etag);
    assert_eq!(
        read.info().uploaded_at,
        Some(UNIX_EPOCH + Duration::from_secs(UPLOADED_AT as u64))
    );
    assert_eq!(&read.bytes().await.expect("the bytes")[..], b"the bytes");
}

#[tokio::test]
async fn a_put_returns_the_etag_of_its_own_write_though_another_write_replaces_the_object_at_once()
{
    let harness = harness("");
    harness
        .fake
        .store
        .rival_lands_after_write
        .store(true, Ordering::SeqCst);

    let written = harness
        .bucket
        .put("contended.txt", "mine")
        .content_type("text/markdown")
        .metadata([("owner", "ada")])
        .await
        .expect("the write");

    assert_eq!(
        harness.fake.store.etags_written("contended.txt"),
        [written.etag.as_str()]
    );
    assert_eq!(written.key, "contended.txt");
    assert_eq!(written.size, 4);
    assert_eq!(written.content_type, "text/markdown");
    assert_eq!(written.metadata["owner"], "ada");
    assert_eq!(written.uploaded_at, None);
}

#[tokio::test]
async fn a_multipart_put_returns_the_etag_of_its_own_write_though_another_write_replaces_the_object_at_once(
) {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);
    harness
        .fake
        .store
        .rival_lands_after_write
        .store(true, Ordering::SeqCst);

    let written = bucket
        .put("contended.bin", vec![b'x'; 26])
        .content_type("application/x-tar")
        .metadata([("owner", "ada")])
        .await
        .expect("the write");

    assert_eq!(
        harness.fake.store.etags_written("contended.bin"),
        [written.etag.as_str()]
    );
    assert_eq!(written.key, "contended.bin");
    assert_eq!(written.size, 26);
    assert_eq!(written.content_type, "application/x-tar");
    assert_eq!(written.metadata["owner"], "ada");
    assert_eq!(written.uploaded_at, None);
}

#[tokio::test]
async fn a_put_makes_no_head_call() {
    let harness = harness("");
    harness
        .bucket
        .put("small.txt", "x")
        .await
        .expect("the write");
    harness
        .bucket
        .clone()
        .with_thresholds(8, 4)
        .put("large.bin", vec![b'x'; 26])
        .await
        .expect("the multipart write");
    let mut writer = harness
        .bucket
        .clone()
        .with_thresholds(8, 4)
        .writer("streamed.bin")
        .await
        .expect("a writer");
    writer.write_all(&[b'x'; 26]).await.expect("the write");
    writer.shutdown().await.expect("the shutdown");

    assert_eq!(harness.fake.store.heads.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn a_put_with_no_media_type_is_written_as_octet_stream() {
    let harness = harness("");
    let written = harness
        .bucket
        .put("untyped.bin", "x")
        .await
        .expect("the write");

    assert_eq!(written.content_type, "application/octet-stream");
    assert_eq!(
        harness
            .fake
            .store
            .object("untyped.bin")
            .expect("the stored object")
            .content_type,
        "application/octet-stream"
    );
}

#[tokio::test]
async fn a_write_the_store_answers_without_its_etag_fails_naming_the_key() {
    let harness = harness("");
    harness
        .fake
        .store
        .withhold_etag
        .store(true, Ordering::SeqCst);

    let err = harness
        .bucket
        .put("unanswered.txt", "x")
        .await
        .expect_err("the store kept the etag back");
    assert_eq!(
        err.to_string(),
        "'unanswered.txt' was refused by the store: the store answered the write of unanswered.txt without its etag"
    );

    let err = harness
        .bucket
        .clone()
        .with_thresholds(8, 4)
        .put("unanswered.bin", vec![b'x'; 26])
        .await
        .expect_err("the store kept the etag back");
    assert_eq!(
        err.to_string(),
        "'unanswered.bin' was refused by the store: the store answered the write of unanswered.bin without its etag"
    );
}

#[tokio::test]
async fn a_get_never_answers_one_versions_info_with_another_versions_bytes() {
    let harness = harness("");
    harness
        .bucket
        .put("moving.txt", "the first bytes")
        .await
        .expect("the write");
    harness.fake.store.replace_before_reads(1);

    let read = harness.bucket.get("moving.txt").await.expect("the read");
    let replaced = harness
        .fake
        .store
        .object("moving.txt")
        .expect("the stored object");
    assert_eq!(read.info().etag, replaced.etag);
    assert_eq!(read.info().size, replaced.body.len() as u64);
    assert_eq!(&read.bytes().await.expect("the bytes")[..], replaced.body);

    harness.fake.store.replace_before_reads(1);
    let ranged = harness
        .bucket
        .get("moving.txt")
        .range(0..3)
        .await
        .expect("the ranged read");
    let replaced = harness
        .fake
        .store
        .object("moving.txt")
        .expect("the stored object");
    assert_eq!(ranged.info().etag, replaced.etag);
    assert_eq!(
        &ranged.bytes().await.expect("the bytes")[..],
        &replaced.body[..3]
    );

    harness.fake.store.replace_before_reads(usize::MAX);
    let Err(err) = harness.bucket.get("moving.txt").await else {
        panic!("a read of an object replaced under every attempt answered");
    };
    assert!(matches!(err, Error::Changed { .. }), "error = {err}");
    assert_eq!(
        err.to_string(),
        "the object under 'moving.txt' changed while it was read"
    );
}

#[tokio::test]
async fn a_range_reads_only_the_bytes_it_covers() {
    let harness = harness("");
    harness
        .bucket
        .put("ranged.txt", "0123456789")
        .await
        .expect("the write");

    let read = harness
        .bucket
        .get("ranged.txt")
        .range(2..5)
        .await
        .expect("the read");
    assert_eq!(read.info().size, 10);
    assert_eq!(&read.bytes().await.expect("the bytes")[..], b"234");
}

#[tokio::test]
async fn a_read_of_a_key_the_bucket_does_not_have_names_the_key() {
    let harness = harness("");
    let Err(err) = harness.bucket.get("absent.txt").await else {
        panic!("a read of a key with no object under it answered");
    };
    assert_eq!(
        err.to_string(),
        "the bucket has no object under 'absent.txt'"
    );
    assert!(matches!(err, Error::NotFound { .. }));
}

#[tokio::test]
async fn a_head_of_a_key_the_bucket_does_not_have_answers_with_nothing() {
    let harness = harness("");
    assert!(harness
        .bucket
        .head("absent.txt")
        .await
        .expect("a head")
        .is_none());
    assert!(!harness
        .bucket
        .exists("absent.txt")
        .await
        .expect("an existence"));
}

#[tokio::test]
async fn a_write_conditioned_on_the_key_being_free_is_refused_when_it_is_not() {
    let harness = harness("");
    harness
        .bucket
        .put("once.txt", "first")
        .if_not_exists()
        .await
        .expect("the first write");

    let err = harness
        .bucket
        .put("once.txt", "second")
        .if_not_exists()
        .await
        .expect_err("the key is taken");
    assert_eq!(
        err.to_string(),
        "the object under 'once.txt' did not meet the condition this write set"
    );
    assert!(matches!(err, Error::PreconditionFailed { .. }));

    let written = harness
        .bucket
        .put("once.txt", "third")
        .if_match(
            &harness
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

    let err = harness
        .bucket
        .put("once.txt", "fourth")
        .if_match("\"stale\"")
        .await
        .expect_err("the etag moved on");
    assert!(matches!(err, Error::PreconditionFailed { .. }));
}

#[tokio::test]
async fn a_listing_pages_through_the_store_without_the_caller_seeing_a_cursor() {
    let harness = harness("");
    for index in 0..5 {
        harness
            .bucket
            .put(&format!("photos/{index}.txt"), "x")
            .await
            .expect("the write");
    }
    harness
        .bucket
        .put("notes/one.txt", "x")
        .await
        .expect("the write");

    let walked: Vec<String> = harness
        .bucket
        .list()
        .prefix("photos/")
        .limit(2)
        .into_stream()
        .map(|listed| listed.expect("an object").key)
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
async fn a_delete_of_a_key_the_bucket_does_not_have_is_not_an_error() {
    let harness = harness("");
    harness
        .bucket
        .put("gone.txt", "x")
        .await
        .expect("the write");

    harness
        .bucket
        .delete_many(["gone.txt", "never-there.txt"])
        .await
        .expect("the delete");
    assert!(!harness.bucket.exists("gone.txt").await.expect("a head"));
    harness
        .bucket
        .delete("never-there.txt")
        .await
        .expect("the second delete");
}

#[tokio::test]
async fn a_copy_leaves_the_same_bytes_under_the_new_key() {
    let harness = harness("");
    harness
        .bucket
        .put("from.txt", "original")
        .await
        .expect("the write");

    let copied = harness
        .bucket
        .copy("from.txt", "to.txt")
        .await
        .expect("the copy");
    assert_eq!(copied.key, "to.txt");
    assert_eq!(
        &harness
            .bucket
            .get("to.txt")
            .await
            .expect("the read")
            .bytes()
            .await
            .expect("the bytes")[..],
        b"original"
    );
}

#[tokio::test]
async fn a_body_too_large_for_one_request_goes_up_in_parts() {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);
    let body: Vec<u8> = (0..26u8).map(|index| b'a' + index).collect();

    let written = bucket
        .put("big.bin", body.clone())
        .await
        .expect("the write");
    assert_eq!(written.size, 26);
    assert_eq!(
        harness
            .fake
            .store
            .object("big.bin")
            .expect("the stored object")
            .body,
        body
    );
    assert_eq!(
        harness.fake.store.most_parts_at_once.load(Ordering::SeqCst),
        super::PARTS_IN_FLIGHT,
        "the parts did not go up four at a time"
    );
    assert!(harness
        .fake
        .store
        .aborted
        .lock()
        .expect("the aborts")
        .is_empty());
}

#[tokio::test]
async fn a_part_the_store_refuses_throws_the_whole_upload_away() {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);
    harness
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
        harness
            .fake
            .store
            .aborted
            .lock()
            .expect("the aborts")
            .as_slice(),
        ["upload-for-doomed.bin"]
    );
    assert!(harness.fake.store.object("doomed.bin").is_none());
}

#[tokio::test]
async fn a_completion_the_store_refuses_throws_the_whole_upload_away() {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);
    harness
        .fake
        .store
        .refuse_complete
        .store(true, Ordering::SeqCst);

    let err = bucket
        .put("refused.bin", vec![b'x'; 26])
        .await
        .expect_err("the store refuses to complete the upload");
    assert!(matches!(err, Error::Refused { .. }), "error = {err}");
    assert_eq!(
        harness
            .fake
            .store
            .aborted
            .lock()
            .expect("the aborts")
            .as_slice(),
        ["upload-for-refused.bin"],
        "a completion that failed left its parts costing storage forever"
    );
    assert!(harness.fake.store.object("refused.bin").is_none());
}

#[tokio::test]
async fn a_reader_and_a_writer_stream_the_bytes() {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);

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
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);

    let mut writer = bucket.writer("abandoned.bin").await.expect("a writer");
    for _ in 0..4 {
        writer.write_all(b"abcdefg").await.expect("the write");
    }
    writer.write_all(b"hijklmn").await.expect("the write");
    drop(writer);
    tokio::task::yield_now().await;
    tokio::time::sleep(Duration::from_millis(50)).await;

    assert_eq!(
        harness
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
    let harness = harness("https://storage.shop.example/store");
    harness
        .bucket
        .put("shared.txt", "x")
        .await
        .expect("the write");

    let url = harness
        .bucket
        .signed_url("shared.txt")
        .expires_in(Duration::from_secs(60))
        .download("shared.txt")
        .await
        .expect("a signed url");
    assert_eq!(url, format!("{}/o/shared.txt", harness.fake.address));

    let upload = harness
        .bucket
        .signed_upload("shared.txt")
        .expires_in(Duration::from_secs(60))
        .max_size(1024)
        .content_type("text/plain")
        .await
        .expect("a signed upload");
    assert_eq!(upload.method, "POST");
    assert_eq!(upload.url, format!("{}/o/shared.txt", harness.fake.address));

    assert_eq!(
        harness
            .bucket
            .public_url("shared.txt")
            .expect("a public url"),
        "https://storage.shop.example/store/shared.txt"
    );
    assert_eq!(
        harness
            .bucket
            .public_url("a b/c#d?e.png")
            .expect("a public url"),
        "https://storage.shop.example/store/a%20b/c%23d%3Fe.png"
    );
}

#[tokio::test]
async fn a_read_hands_back_the_bytes_as_they_arrive() {
    let harness = harness("");
    harness
        .bucket
        .put("streamed.txt", "arriving")
        .await
        .expect("the write");

    let read = harness.bucket.get("streamed.txt").await.expect("the read");
    let mut arrived = Vec::new();
    let mut chunks = Box::pin(read.into_stream());
    while let Some(chunk) = chunks.next().await {
        arrived.extend_from_slice(&chunk.expect("a chunk"));
    }
    assert_eq!(arrived, b"arriving");
}

#[tokio::test]
async fn a_bucket_reached_during_discovery_says_it_is_not_provisioned_yet() {
    let harness = harness("");
    std::env::set_var("OCEL_PHASE", "discovery");
    let reached = harness.bucket.head("anything.txt").await;
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
    let harness = harness("");
    let err = harness
        .bucket
        .public_url("shared.txt")
        .expect_err("no public address");
    assert!(matches!(err, Error::NotPublic { .. }), "error = {err}");
    assert!(err.to_string().contains("#[ocel(public)]"), "error = {err}");
}

#[tokio::test]
async fn an_object_is_written_and_read_through_an_https_store() {
    let harness = harness_of(start_with_https_objects(), "");
    let bucket = harness.bucket.clone().trusting(read_trusted_roots());

    bucket
        .put("avatars/one.png", "the bytes")
        .await
        .expect("the write");
    let read = bucket.get("avatars/one.png").await.expect("the read");
    assert_eq!(&read.bytes().await.expect("the bytes")[..], b"the bytes");

    let parts = bucket.clone().with_thresholds(8, 4);
    let body: Vec<u8> = (0..26u8).map(|index| b'a' + index).collect();
    parts
        .put("big.bin", body.clone())
        .await
        .expect("the multipart write");
    assert_eq!(
        harness
            .fake
            .store
            .object("big.bin")
            .expect("the stored object")
            .body,
        body
    );
}

#[tokio::test]
async fn a_store_that_cannot_be_reached_is_named_by_the_key_and_never_by_its_signed_url() {
    let harness = harness_of(start_with_https_objects(), "");

    let err = harness
        .bucket
        .put("avatars/one.png", "the bytes")
        .await
        .expect_err("the store's certificate is not trusted");
    let Error::Refused { key, .. } = &err else {
        panic!("error = {err}");
    };
    assert_eq!(key, "avatars/one.png");
    assert!(!err.to_string().contains("signature"), "error = {err}");
}

#[tokio::test]
async fn a_writer_whose_part_failed_fails_its_shutdown() {
    let harness = harness("");
    let bucket = harness.bucket.clone().with_thresholds(8, 4);
    harness
        .fake
        .store
        .refuse_parts
        .store(true, Ordering::SeqCst);

    let mut writer = bucket.writer("doomed.bin").await.expect("a writer");
    for _ in 0..4 {
        let _ = writer.write_all(b"abcdefg").await;
    }
    writer
        .shutdown()
        .await
        .expect_err("the shutdown of a writer whose part failed");
    assert!(harness.fake.store.object("doomed.bin").is_none());
}
