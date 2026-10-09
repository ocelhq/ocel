# Bucket

Object storage for files. In `ocel dev` it runs in Docker.
- A `public` bucket serves every object anonymously over HTTP.
- `allowedOrigins` (`allowed_origins`) names the browser origins allowed to upload straight to the store.

## Declare, write, read

```ts
// a file in the discovery folder
import { bucket } from "ocel/bucket"; // ocel/bucket/next | /hono | /express add upload routes
export const uploads = bucket("uploads");

await uploads.put("reports/q3.pdf", bytes, { contentType: "application/pdf" });
const object = await uploads.get("reports/q3.pdf"); // null when missing
if (object) await object.bytes();
for await (const o of uploads.list({ prefix: "reports/" })) console.log(o.key);
const url = await uploads.signedUrl("reports/q3.pdf");
```

```go
var Uploads = ocel.Bucket("uploads") // ocel.BucketPublic(), ocel.BucketAllowedOrigins(...)

err := infra.Uploads.WriteAll(ctx, "reports/q3.pdf", data)
data, err := infra.Uploads.ReadAll(ctx, "reports/q3.pdf") // errors.Is(err, ocel.ErrObjectNotFound)
for obj, err := range infra.Uploads.List(ctx) { /* … */ }
url, err := infra.Uploads.SignedURL(ctx, "reports/q3.pdf")
// NewWriter / NewReader stream large objects
```

```python
uploads = ocel.bucket("uploads")  # public=True, allowed_origins=[...]

uploads.put("reports/q3.pdf", data, content_type="application/pdf")
body = uploads.get("reports/q3.pdf")  # raises ocel.ObjectNotFound when missing
for info in uploads.list(prefix="reports/"): ...
url = uploads.signed_url("reports/q3.pdf")
# every method has an _async twin: await uploads.put_async(...)
```

```rust
#[ocel(name = "uploads")] // public, allowed_origins = ["https://..."]
pub uploads: ocel::Bucket,

infra.uploads.put("reports/q3.pdf", bytes).await?;
let object = infra.uploads.get("reports/q3.pdf").await?;
```

## Uploads from the browser

- **TypeScript upload routes:** declare `bucket(name, { uploaders: { … } })` with `uploader()` from `ocel/bucket/next`, `/hono` or `/express`. The browser side uses `createUploadClient` from `ocel/bucket/client`.
- **Presigning, in every SDK:** `signedUpload` returns the URL, method and fields for a direct upload.
- **Origins:** a direct browser upload needs the page's origin in `allowedOrigins`.

The SDK's own types and docstrings are the full API.
