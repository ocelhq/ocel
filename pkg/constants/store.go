package constants

const objectStoreImage = "rustfs/rustfs:1.0.0@sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff"

func ObjectStoreImage() string { return objectStoreImage }

const ReservedKeyPrefix = ProjectStateDirName + "/"

const storeSessionsBucket = "ocel-sessions"

func StoreSessionsBucket() string { return storeSessionsBucket }
