pub(crate) const KIND: &str = "bucket";

/// A bucket an app declares and reads and writes its objects through. A field of this type
/// in a struct deriving [`Resources`](macro@crate::Resources) is the declaration.
#[derive(Clone)]
pub struct Bucket {
    name: String,
}

impl Bucket {
    /// Take the handle for the bucket named `name`. Prefer
    /// [`Resources`](macro@crate::Resources), which declares the bucket as well as handing
    /// back its handle.
    pub fn new(name: impl Into<String>) -> Self {
        Self { name: name.into() }
    }

    /// The name the bucket was declared under, and the name its binding is delivered as.
    pub fn name(&self) -> &str {
        &self.name
    }
}
