import { fixtureEnv, migrate } from "../../../ocel/index";

export async function POST(request: Request) {
  if (
    request.headers.get("authorization") !==
    `Bearer ${fixtureEnv.FIXTURE_BOOTSTRAP_TOKEN}`
  ) {
    return new Response(null, { status: 404 });
  }
  await migrate();
  return new Response(null, { status: 204 });
}
