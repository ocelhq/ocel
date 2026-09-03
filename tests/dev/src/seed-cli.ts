import { seed } from "@ocel-tests/shared/seed";

async function main() {
  const { token, userId, organizationId } = await seed("E2E");
  process.stderr.write(
    `seeded user=${userId} org=${organizationId}\n`,
  );
  process.stdout.write(token);
  process.stdout.write("\n");
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
