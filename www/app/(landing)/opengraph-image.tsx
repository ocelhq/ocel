import { ogContentType, ogImage, ogSize } from "@/lib/og";

export const alt = "Ocel: deploy apps anywhere.";
export const size = ogSize;
export const contentType = ogContentType;

export default function Image() {
  return ogImage({
    title: "Deploy apps anywhere.",
    description:
      "One CLI ships your app to AWS, Google Cloud, a VPS or Ocel Cloud. Your code declares the resources it needs, and Ocel builds them too.",
  });
}
