import { CubeIcon, RocketLaunchIcon, ServerStackIcon } from "@heroicons/react/24/outline";
import { Card, Cards } from "fumadocs-ui/components/card";

export function StartCards() {
  return (
    <Cards className="gap-4 lg:grid-cols-3 max-lg:[&>a:last-child:nth-child(odd)]:col-span-full">
      <Card
        icon={<RocketLaunchIcon />}
        title="Quick start"
        href="/docs/quick-start"
        description="Deploy a Next.js app to AWS in minutes."
      />
      <Card
        icon={<ServerStackIcon />}
        title="Providers"
        href="/docs/providers/aws"
        description="Explore targets you can deploy your apps to"
      />
      <Card
        icon={<CubeIcon />}
        title="Frameworks"
        href="/docs/nextjs"
        description="Explore supported frameworks and languages."
      />
    </Cards>
  );
}
