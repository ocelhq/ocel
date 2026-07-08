import { seed } from "./seed";

// CI entry point: mint an identity and print ONLY the bearer token to stdout,
// so the workflow can capture it into OCEL_ACCESS_TOKEN. Diagnostics go to
// stderr to keep stdout clean for the capture.
async function main() {
  const { token, userId, organizationId } = await seed();
  process.stderr.write(
    `seeded user=${userId} org=${organizationId}\n`,
  );
  process.stdout.write(token);
  process.stdout.write("\n");
  // signUpEmail/createOrganization leave a pg pool open via @repo/db.
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
