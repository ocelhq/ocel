import Image from "next/image";

import photo from "./photo.png";

// A static import, so the build emits the image under /_next/static/media and
// the image conformance fixtures have a real static-import path to exercise
// (it is the one path that gets an immutable Cache-Control).
export default function ImageFixturesPage() {
  return <Image src={photo} alt="image fixture source" />;
}
