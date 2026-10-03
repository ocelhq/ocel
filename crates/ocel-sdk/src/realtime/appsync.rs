use super::transport::send_request;
use bytes::Bytes;
use ring::{digest, hmac};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::sync::Mutex;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

const CONTAINER_CREDENTIALS_ORIGIN: &str = "http://169.254.170.2";
const REFRESH_BEFORE_EXPIRY: Duration = Duration::from_secs(300);
const CREDENTIALS_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct AwsCredentials {
    pub(crate) access_key_id: String,
    pub(crate) secret_access_key: String,
    pub(crate) session_token: Option<String>,
}

static HELD_CONTAINER_CREDENTIALS: Mutex<Option<(AwsCredentials, SystemTime)>> = Mutex::new(None);

fn read_env(name: &str) -> Option<String> {
    std::env::var(name).ok().filter(|value| !value.is_empty())
}

fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let year_of_era = year - era * 400;
    let day_of_year = (153 * (month + if month > 2 { -3 } else { 9 }) + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let days = days + 719_468;
    let era = days.div_euclid(146_097);
    let day_of_era = days - era * 146_097;
    let year_of_era =
        (day_of_era - day_of_era / 1460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_index = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_index + 2) / 5 + 1;
    let month = if month_index < 10 {
        month_index + 3
    } else {
        month_index - 9
    };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

fn format_amz_date(at: SystemTime) -> String {
    let seconds = at
        .duration_since(UNIX_EPOCH)
        .map_or(0, |since| since.as_secs() as i64);
    let (year, month, day) = civil_from_days(seconds.div_euclid(86_400));
    let of_day = seconds.rem_euclid(86_400);
    format!(
        "{year:04}{month:02}{day:02}T{:02}{:02}{:02}Z",
        of_day / 3600,
        of_day / 60 % 60,
        of_day % 60
    )
}

fn parse_expiration(raw: &str) -> Option<SystemTime> {
    let field = |range: std::ops::Range<usize>| raw.get(range)?.parse::<i64>().ok();
    let days = days_from_civil(field(0..4)?, field(5..7)?, field(8..10)?);
    let seconds = days * 86_400 + field(11..13)? * 3600 + field(14..16)? * 60 + field(17..19)?;
    Some(UNIX_EPOCH + Duration::from_secs(u64::try_from(seconds).ok()?))
}

#[cfg(test)]
pub(crate) fn forget_container_credentials() {
    *HELD_CONTAINER_CREDENTIALS.lock().expect("credentials lock") = None;
}

async fn read_container_credentials(endpoint: &str) -> Result<AwsCredentials, String> {
    let held = HELD_CONTAINER_CREDENTIALS
        .lock()
        .expect("credentials lock")
        .clone();
    if let Some((credentials, expires)) = held {
        if expires
            .duration_since(SystemTime::now())
            .is_ok_and(|left| left > REFRESH_BEFORE_EXPIRY)
        {
            return Ok(credentials);
        }
    }
    let token = match read_env("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE") {
        Some(file) => Some(
            std::fs::read_to_string(&file)
                .map_err(|err| format!("read the container credentials token: {err}"))?
                .trim()
                .to_string(),
        ),
        None => read_env("AWS_CONTAINER_AUTHORIZATION_TOKEN"),
    };
    let headers: Vec<(String, String)> = token
        .map(|token| ("authorization".to_string(), token))
        .into_iter()
        .collect();
    let uri: http::Uri = endpoint
        .parse()
        .map_err(|_| format!("the container credentials endpoint '{endpoint}' is no URL"))?;
    let (status, body) = tokio::time::timeout(
        CREDENTIALS_TIMEOUT,
        send_request(http::Method::GET, &uri, &headers, Bytes::new()),
    )
    .await
    .map_err(|_| "the container credentials endpoint did not answer".to_string())?
    .map_err(|err| format!("read the container's AWS credentials: {err}"))?;
    if !(200..300).contains(&status) {
        return Err(format!(
            "the container credentials endpoint answered status {status}"
        ));
    }
    let answered: serde_json::Value = serde_json::from_slice(&body)
        .map_err(|_| "the container credentials endpoint answered no JSON".to_string())?;
    let field = |name: &str| {
        answered
            .get(name)
            .and_then(serde_json::Value::as_str)
            .filter(|value| !value.is_empty())
            .map(ToString::to_string)
    };
    let (Some(access_key_id), Some(secret_access_key)) =
        (field("AccessKeyId"), field("SecretAccessKey"))
    else {
        return Err("the container's credentials endpoint answered no AWS credentials".to_string());
    };
    let credentials = AwsCredentials {
        access_key_id,
        secret_access_key,
        session_token: field("Token"),
    };
    let expires = field("Expiration")
        .and_then(|raw| parse_expiration(&raw))
        .unwrap_or(UNIX_EPOCH);
    *HELD_CONTAINER_CREDENTIALS.lock().expect("credentials lock") =
        Some((credentials.clone(), expires));
    Ok(credentials)
}

pub(crate) async fn read_aws_credentials() -> Result<AwsCredentials, String> {
    if let (Some(access_key_id), Some(secret_access_key)) = (
        read_env("AWS_ACCESS_KEY_ID"),
        read_env("AWS_SECRET_ACCESS_KEY"),
    ) {
        return Ok(AwsCredentials {
            access_key_id,
            secret_access_key,
            session_token: read_env("AWS_SESSION_TOKEN"),
        });
    }
    if let Some(relative) = read_env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") {
        return read_container_credentials(&format!("{CONTAINER_CREDENTIALS_ORIGIN}{relative}"))
            .await;
    }
    let endpoint = read_env("AWS_CONTAINER_CREDENTIALS_FULL_URI").ok_or_else(|| {
        "no AWS credentials to sign an AppSync publish with: set AWS_ACCESS_KEY_ID and \
         AWS_SECRET_ACCESS_KEY, or run where AWS_CONTAINER_CREDENTIALS_RELATIVE_URI is delivered"
            .to_string()
    })?;
    refuse_untrusted_credentials_endpoint(&endpoint)?;
    read_container_credentials(&endpoint).await
}

const CONTAINER_CREDENTIALS_IPS: [IpAddr; 3] = [
    IpAddr::V4(Ipv4Addr::new(169, 254, 170, 2)),
    IpAddr::V4(Ipv4Addr::new(169, 254, 170, 23)),
    IpAddr::V6(Ipv6Addr::new(0xfd00, 0xec2, 0, 0, 0, 0, 0, 0x23)),
];

fn is_container_credentials_host(host: &str) -> bool {
    if host == "localhost" {
        return true;
    }
    host.trim_start_matches('[')
        .trim_end_matches(']')
        .parse::<IpAddr>()
        .is_ok_and(|ip| ip.is_loopback() || CONTAINER_CREDENTIALS_IPS.contains(&ip))
}

fn refuse_untrusted_credentials_endpoint(endpoint: &str) -> Result<(), String> {
    let uri: http::Uri = endpoint
        .parse()
        .map_err(|_| "AWS_CONTAINER_CREDENTIALS_FULL_URI is no URL".to_string())?;
    let scheme = uri.scheme_str().unwrap_or_default();
    let host = uri.host().unwrap_or_default();
    if scheme == "https" || scheme == "http" && is_container_credentials_host(host) {
        return Ok(());
    }
    Err(format!(
        "AWS_CONTAINER_CREDENTIALS_FULL_URI names {scheme}://{host}, and only https or a \
         loopback, ECS or EKS address over http is asked for credentials"
    ))
}

pub(crate) fn find_appsync_region(host: &str) -> String {
    host.split_once(".appsync-api.")
        .and_then(|(_, rest)| rest.split_once('.'))
        .map(|(region, _)| region.to_string())
        .filter(|region| !region.is_empty())
        .or_else(|| read_env("AWS_REGION"))
        .unwrap_or_default()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn hash_sha256(data: &[u8]) -> String {
    hex(digest::digest(&digest::SHA256, data).as_ref())
}

fn sign_hmac(key: &[u8], data: &str) -> Vec<u8> {
    hmac::sign(&hmac::Key::new(hmac::HMAC_SHA256, key), data.as_bytes())
        .as_ref()
        .to_vec()
}

pub(crate) fn sign_appsync_publish(
    host: &str,
    body: &[u8],
    credentials: &AwsCredentials,
    region: &str,
    at: SystemTime,
) -> Vec<(String, String)> {
    let stamp = format_amz_date(at);
    let day = &stamp[..8];
    let mut signed = vec![
        ("content-type", "application/json".to_string()),
        ("host", host.to_string()),
        ("x-amz-date", stamp.clone()),
    ];
    if let Some(token) = &credentials.session_token {
        signed.push(("x-amz-security-token", token.clone()));
    }
    let signed_headers = signed
        .iter()
        .map(|(name, _)| *name)
        .collect::<Vec<_>>()
        .join(";");
    let canonical_headers: String = signed
        .iter()
        .map(|(name, value)| format!("{name}:{value}\n"))
        .collect();
    let canonical_request = [
        "POST",
        "/event",
        "",
        &canonical_headers,
        &signed_headers,
        &hash_sha256(body),
    ]
    .join("\n");
    let scope = format!("{day}/{region}/appsync/aws4_request");
    let string_to_sign = [
        "AWS4-HMAC-SHA256",
        &stamp,
        &scope,
        &hash_sha256(canonical_request.as_bytes()),
    ]
    .join("\n");
    let mut key = sign_hmac(
        format!("AWS4{}", credentials.secret_access_key).as_bytes(),
        day,
    );
    for part in [region, "appsync", "aws4_request"] {
        key = sign_hmac(&key, part);
    }
    let signature = hex(&sign_hmac(&key, &string_to_sign));
    let mut headers = vec![
        ("content-type".to_string(), "application/json".to_string()),
        ("x-amz-date".to_string(), stamp),
    ];
    if let Some(token) = &credentials.session_token {
        headers.push(("x-amz-security-token".to_string(), token.clone()));
    }
    headers.push((
        "authorization".to_string(),
        format!(
            "AWS4-HMAC-SHA256 Credential={}/{scope}, SignedHeaders={signed_headers}, Signature={signature}",
            credentials.access_key_id
        ),
    ));
    headers
}

pub(crate) fn build_appsync_publish_url(host: &str) -> Option<http::Uri> {
    format!("https://{host}/event").parse().ok()
}

pub(crate) async fn publish_to_appsync(
    endpoint: &http::Uri,
    host: &str,
    channel: &str,
    envelope: Bytes,
) -> Result<(), String> {
    let credentials = read_aws_credentials().await?;
    let envelope = std::str::from_utf8(&envelope).map_err(|err| err.to_string())?;
    let body = serde_json::to_vec(&serde_json::json!({ "channel": channel, "events": [envelope] }))
        .map_err(|err| err.to_string())?;
    let headers = sign_appsync_publish(
        host,
        &body,
        &credentials,
        &find_appsync_region(host),
        SystemTime::now(),
    );
    let (status, answer) =
        send_request(http::Method::POST, endpoint, &headers, Bytes::from(body)).await?;
    let said = String::from_utf8_lossy(&answer);
    if !(200..300).contains(&status) {
        return Err(format!("AppSync refused it with status {status}: {said}"));
    }
    let failed = serde_json::from_slice::<serde_json::Value>(&answer)
        .ok()
        .and_then(|answer| answer.get("failed")?.as_array().map(Vec::len))
        .unwrap_or(0);
    if failed > 0 {
        return Err(format!("AppSync failed the event: {said}"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    const BODY: &str = r#"{"channel":"/app/orders/o1","events":["{\"v\":1}"]}"#;

    fn keys(session_token: Option<&str>) -> AwsCredentials {
        AwsCredentials {
            access_key_id: "AKIDEXAMPLE".to_string(),
            secret_access_key: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY".to_string(),
            session_token: session_token.map(ToString::to_string),
        }
    }

    fn header<'a>(headers: &'a [(String, String)], name: &str) -> Option<&'a str> {
        headers
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }

    #[test]
    fn a_publish_is_signed_as_the_aws_sdk_signs_it() {
        let at = UNIX_EPOCH + Duration::from_secs(1_791_028_800);
        let host = "abc123.appsync-api.eu-west-1.amazonaws.com";

        let plain = sign_appsync_publish(host, BODY.as_bytes(), &keys(None), "eu-west-1", at);
        let session = sign_appsync_publish(
            host,
            BODY.as_bytes(),
            &keys(Some("session-token")),
            "eu-west-1",
            at,
        );

        assert_eq!(header(&plain, "x-amz-date"), Some("20261003T120000Z"));
        assert_eq!(
            header(&plain, "authorization"),
            Some("AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=6c8f1bf8968ad3ec649127bc436717dd96a143637d076bfe6013567da6855ae1")
        );
        assert_eq!(
            header(&session, "x-amz-security-token"),
            Some("session-token")
        );
        assert_eq!(
            header(&session, "authorization"),
            Some("AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=aa66eda43423cb89f230afcbf748369d3decaa2a23ef192d0b4a3d41087c747b")
        );
    }

    #[test]
    fn a_containers_expiration_is_read_as_the_instant_it_names() {
        assert_eq!(
            parse_expiration("2026-10-03T12:00:00Z"),
            Some(UNIX_EPOCH + Duration::from_secs(1_791_028_800))
        );
        assert_eq!(
            format_amz_date(UNIX_EPOCH + Duration::from_secs(951_782_400)),
            "20000229T000000Z"
        );
        assert_eq!(parse_expiration("not a date"), None);
    }

    struct Received {
        method: String,
        path: String,
        headers: http::HeaderMap,
        body: Bytes,
    }

    async fn serve_once(
        status: u16,
        answer: &'static str,
    ) -> (String, tokio::sync::oneshot::Receiver<Received>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let host = listener.local_addr().unwrap().to_string();
        let (tell, heard) = tokio::sync::oneshot::channel();
        tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let tell = Mutex::new(Some(tell));
            let service =
                hyper::service::service_fn(move |request: http::Request<hyper::body::Incoming>| {
                    let tell = tell.lock().unwrap().take();
                    async move {
                        let (parts, body) = request.into_parts();
                        let body = http_body_util::BodyExt::collect(body)
                            .await
                            .unwrap()
                            .to_bytes();
                        if let Some(tell) = tell {
                            let _ = tell.send(Received {
                                method: parts.method.to_string(),
                                path: parts.uri.path().to_string(),
                                headers: parts.headers,
                                body,
                            });
                        }
                        Ok::<_, std::convert::Infallible>(
                            http::Response::builder()
                                .status(status)
                                .body(http_body_util::Full::new(Bytes::from_static(
                                    answer.as_bytes(),
                                )))
                                .unwrap(),
                        )
                    }
                });
            let _ = hyper::server::conn::http1::Builder::new()
                .serve_connection(hyper_util::rt::TokioIo::new(stream), service)
                .await;
        });
        (host, heard)
    }

    #[tokio::test]
    async fn a_publish_reaches_appsync_signed_with_the_role_and_fails_on_a_failed_event() {
        std::env::set_var("AWS_ACCESS_KEY_ID", "ASIAAPPROLE");
        std::env::set_var("AWS_SECRET_ACCESS_KEY", "role-secret");
        std::env::set_var("AWS_SESSION_TOKEN", "role-session");
        std::env::set_var("AWS_REGION", "eu-west-1");

        let (host, heard) = serve_once(200, r#"{"successful":[{"index":0}],"failed":[]}"#).await;
        let endpoint: http::Uri = format!("http://{host}/event").parse().unwrap();
        publish_to_appsync(
            &endpoint,
            &host,
            "/app/orders/o1",
            Bytes::from_static(br#"{"v":1}"#),
        )
        .await
        .unwrap();
        let received = heard.await.unwrap();
        assert_eq!(
            (received.method.as_str(), received.path.as_str()),
            ("POST", "/event")
        );
        let body: serde_json::Value = serde_json::from_slice(&received.body).unwrap();
        assert_eq!(
            body,
            serde_json::json!({"channel": "/app/orders/o1", "events": [r#"{"v":1}"#]})
        );
        let authorization = received.headers["authorization"].to_str().unwrap();
        assert!(authorization.starts_with("AWS4-HMAC-SHA256 Credential=ASIAAPPROLE/"));
        assert!(authorization.contains("/eu-west-1/appsync/aws4_request"));
        assert_eq!(received.headers["x-amz-security-token"], "role-session");

        let (host, _heard) = serve_once(
            200,
            r#"{"successful":[],"failed":[{"index":0,"message":"too large"}]}"#,
        )
        .await;
        let endpoint: http::Uri = format!("http://{host}/event").parse().unwrap();
        let failed = publish_to_appsync(
            &endpoint,
            &host,
            "/app/orders/o1",
            Bytes::from_static(b"{}"),
        )
        .await;
        assert!(matches!(failed, Err(said) if said.contains("too large")));

        let (host, _heard) = serve_once(403, r#"{"errors":[]}"#).await;
        let endpoint: http::Uri = format!("http://{host}/event").parse().unwrap();
        let refused = publish_to_appsync(
            &endpoint,
            &host,
            "/app/orders/o1",
            Bytes::from_static(b"{}"),
        )
        .await;
        assert!(matches!(refused, Err(said) if said.contains("status 403")));

        std::env::remove_var("AWS_ACCESS_KEY_ID");
        std::env::remove_var("AWS_SECRET_ACCESS_KEY");
        std::env::remove_var("AWS_SESSION_TOKEN");
        let (host, heard) = serve_once(
            200,
            r#"{"AccessKeyId":"ASIACONTAINER","SecretAccessKey":"s","Token":"container-session","Expiration":"2999-01-01T00:00:00Z"}"#,
        )
        .await;
        std::env::set_var(
            "AWS_CONTAINER_CREDENTIALS_FULL_URI",
            format!("http://{host}/creds"),
        );
        std::env::set_var("AWS_CONTAINER_AUTHORIZATION_TOKEN", "container-token");
        forget_container_credentials();
        for _ in 0..3 {
            let credentials = read_aws_credentials().await.unwrap();
            assert_eq!(credentials.access_key_id, "ASIACONTAINER");
            assert_eq!(
                credentials.session_token.as_deref(),
                Some("container-session")
            );
        }
        assert_eq!(
            heard.await.unwrap().headers["authorization"],
            "container-token"
        );
        std::env::set_var(
            "AWS_CONTAINER_CREDENTIALS_FULL_URI",
            "http://credentials.invalid/creds",
        );
        forget_container_credentials();
        let refused = read_aws_credentials().await;
        assert!(
            matches!(&refused, Err(said) if said.contains("AWS_CONTAINER_CREDENTIALS_FULL_URI")),
            "{refused:?}"
        );
        std::env::remove_var("AWS_CONTAINER_CREDENTIALS_FULL_URI");
        std::env::remove_var("AWS_CONTAINER_AUTHORIZATION_TOKEN");
        forget_container_credentials();
        let missing = read_aws_credentials().await;
        assert!(matches!(missing, Err(said) if said.contains("AWS_ACCESS_KEY_ID")));
    }

    #[test]
    fn only_a_loopback_container_or_https_credentials_endpoint_is_trusted_with_the_token() {
        for endpoint in [
            "http://localhost:9000/v2",
            "http://127.0.0.1/v2",
            "http://127.8.9.10/v2",
            "http://[::1]/v2",
            "http://169.254.170.2/v2",
            "http://169.254.170.23/v1",
            "http://[fd00:ec2::23]/v1",
            "https://credentials.example.com/v2",
        ] {
            assert_eq!(
                refuse_untrusted_credentials_endpoint(endpoint),
                Ok(()),
                "{endpoint}"
            );
        }
        for endpoint in [
            "http://credentials.example.com/v2",
            "http://10.0.0.5/v2",
            "http://169.254.169.254/latest",
            "http://127.0.0.1.example.com/v2",
            "ftp://127.0.0.1/v2",
        ] {
            assert!(
                matches!(refuse_untrusted_credentials_endpoint(endpoint), Err(said) if said.contains("AWS_CONTAINER_CREDENTIALS_FULL_URI")),
                "{endpoint}"
            );
        }
    }

    #[test]
    fn the_region_is_read_from_the_apis_host() {
        assert_eq!(
            find_appsync_region("abc123.appsync-api.eu-west-1.amazonaws.com"),
            "eu-west-1"
        );
    }
}
