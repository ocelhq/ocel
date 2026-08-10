import Image from "next/image";

import photo from "./photo.png";

export default function ImageFixturesPage() {
  return <Image src={photo} alt="image fixture source" />;
}
