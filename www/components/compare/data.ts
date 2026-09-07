export type Lang = "ts" | "hcl" | "yaml" | "toml" | "json" | "bash" | "txt";

export type Code = { lang: Lang; filename?: string; code: string };

export type Facts = { label: string; value: string }[];

export type Pane = { code?: Code; facts?: Facts; list?: string[] };

export type Row = { key: string; question: string; them: Pane; ocel: Pane; verdict: string };

export type FamilySlug = "hosted" | "iac" | "selfhost";

export type Family = { slug: FamilySlug; name: string; lede: string };

export type Tool = {
  slug: string;
  name: string;
  family: FamilySlug;
  rows: Row[];
};

export const DEFAULT = "vercel";

const QUESTIONS = {
  account: "Whose account does it run in?",
  database: "How do you get a Postgres?",
  deploy: "What do you write to deploy?",
  dev: "How do you develop against it?",
  beside: "Can you keep what you already have?",
  pick: "Which one do you pick?",
};

const configAws: Pane = {
  code: {
    lang: "ts",
    filename: "ocel.config.ts",
    code: `import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "my-app",
  provider: awsProvider(),
});`,
  },
};

const configVps: Pane = {
  code: {
    lang: "ts",
    filename: "ocel.config.ts",
    code: `import { defineConfig } from "ocel/config";
import vpsProvider from "@ocel/provider-vps";

export default defineConfig({
  slug: "my-app",
  provider: vpsProvider({ ssh: "my-vps" }),
});`,
  },
};

const appPostgres: Pane = {
  code: {
    lang: "ts",
    filename: "server.ts",
    code: `import { postgres } from "ocel/postgres";

const db = postgres("main");
const { rows } = await db.query(
  "select * from documents",
);`,
  },
};

const linkedPostgres: Pane = {
  code: {
    lang: "ts",
    filename: "ocel.config.ts",
    code: `export default defineConfig({
  slug: "shop",
  provider: awsProvider(),
  links: ["orders"],
});`,
  },
};

const ocelDevVerdict =
  "Once the project is linked on the console, `ocel dev` runs your framework's own dev command with the real resources resolved, on every machine on the team.";

const ocelDev: Pane = {
  code: { lang: "bash", code: `ocel dev -- next dev` },
};

const ocelDeployCmd = "`ocel deploy`";

function ocelAccount(runsIn: string, billedBy: string, dataIn: string): Pane {
  return {
    facts: [
      { label: "Runs in", value: runsIn },
      { label: "Billed by", value: billedBy },
      { label: "Data lives in", value: dataIn },
      { label: "Vendor's side", value: "Nothing, unless you link the optional console" },
    ],
  };
}

function themAccount(runsIn: string, billedBy: string, dataIn: string, vendor: string): Pane {
  return {
    facts: [
      { label: "Runs in", value: runsIn },
      { label: "Billed by", value: billedBy },
      { label: "Data lives in", value: dataIn },
      { label: "Vendor's side", value: vendor },
    ],
  };
}

const ocelAccountHosted = ocelAccount(
  "Your AWS account or your server",
  "Your provider, to you",
  "Your account",
);
const ocelAccountCloud = ocelAccount("Your AWS account", "AWS, to you", "Your account");
const ocelAccountServer = ocelAccount("Your server", "Your host, to you", "Your server");

const ocelPickOwnAccount = "The account, the bill and the data have to be yours.";
const ocelPickOneLine =
  "You want to move between AWS and a VPS by editing one line rather than rebuilding the deployment.";
const ocelPickAppCode =
  "You want the database named by the code that uses it, with no second definition to keep in sync.";

export const families: Family[] = [
  { slug: "hosted", name: "Hosted platforms", lede: "Same deploy, your account." },
  { slug: "iac", name: "Infrastructure as code", lede: "Beside it, not instead of it." },
  { slug: "selfhost", name: "Self-host tooling", lede: "Your server, one config." },
];

export const tools: Tool[] = [
  {
    slug: "vercel",
    name: "Vercel",
    family: "hosted",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Vercel's infrastructure",
          "Vercel",
          "The Marketplace provider's account",
          "Builds, routing, functions, database credentials",
        ),
        ocel: ocelAccountHosted,
        verdict:
          "You never hold a cloud account with Vercel, and Postgres comes from a Marketplace provider's account rather than yours.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "ts",
            filename: "db.ts",
            code: `import { neon } from "@neondatabase/serverless";

const sql = neon(process.env.DATABASE_URL!);`,
          },
        },
        ocel: appPostgres,
        verdict:
          "You install a Postgres integration from the Marketplace in the dashboard and Vercel injects the credentials as environment variables; the Ocel call creates the database in your account and returns the client.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "json",
            filename: "vercel.json",
            code: `{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "framework": "nextjs",
  "regions": ["iad1"]
}`,
          },
        },
        ocel: configAws,
        verdict:
          "A Next.js app needs no `vercel.json` at all, and `regions` selects among Vercel's own regions; the Ocel config's provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          code: {
            lang: "bash",
            code: `vercel dev
vercel env pull`,
          },
        },
        ocel: ocelDev,
        verdict:
          "`vercel dev` replicates the deployment environment locally and `vercel env pull` writes the variables to `.env`, though Vercel recommends `next dev` when that already covers you; `ocel dev` wraps that same dev command with the resources resolved, once the project is linked on the console.",
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You are shipping a Next.js app and want rendering, routing, ISR and image optimization supported by the people who build the framework integrations.",
            "You want previews, edge routing, a CDN and rollbacks as product features rather than something you assemble and operate.",
            "You do not want a cloud account at all: no IAM, no VPC, no capacity planning.",
          ],
        },
        ocel: {
          list: [
            "The account, the bill and the data have to be yours, because of an existing AWS account, a residency rule or an audit.",
            ocelPickOneLine,
            ocelPickAppCode,
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "railway",
    name: "Railway",
    family: "hosted",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Railway's infrastructure",
          "Railway",
          "Railway's account, as a service in your project",
          "Builds, deploys, the Postgres image, project networking",
        ),
        ocel: ocelAccountHosted,
        verdict: "The project and its Postgres sit in Railway's account rather than one you hold.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "ts",
            filename: ".railway/railway.ts",
            code: `import { defineRailway, postgres, project, service } from "railway/iac";

export default defineRailway(() => {
  const db = postgres("postgres");

  const api = service("api", {
    env: {
      DATABASE_URL: db.env.DATABASE_URL,
    },
  });

  return project("my-app", { resources: [db, api] });
});`,
          },
        },
        ocel: appPostgres,
        verdict:
          "Railway declares the database beside the service and passes `db.env.DATABASE_URL` across; Ocel has no separate declaration, because the call in the app is the declaration.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "ts",
            filename: ".railway/railway.ts",
            code: `import { defineRailway, project, service } from "railway/iac";

export default defineRailway(() => {
  const web = service("web", {
    build: "npm run build",
    start: "npm start",
  });

  return project("my-app", { resources: [web] });
});`,
          },
        },
        ocel: configAws,
        verdict:
          "`railway config apply` plans and applies this file inside a Railway project, and nothing in it names a target; the Ocel config's provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          code: {
            lang: "bash",
            code: `railway run npm run dev
railway shell
railway dev`,
          },
        },
        ocel: ocelDev,
        verdict:
          "`railway run` injects the project's environment variables into a local command; once the project is linked on the console, `ocel dev` resolves the resources your app code asked for instead of a variable list.",
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want services and managed Postgres on one canvas, and would rather add a database from a menu than reason about its provisioning, backups or networking.",
            "You want typed cross-resource references in TypeScript, so an attachment is `db.env.DATABASE_URL` rather than a copied connection string.",
            "You do not want a cloud account of your own to hold and operate.",
          ],
        },
        ocel: { list: [ocelPickOwnAccount, ocelPickOneLine, ocelPickAppCode] },
        verdict: "",
      },
    ],
  },
  {
    slug: "render",
    name: "Render",
    family: "hosted",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Render's infrastructure, in Render's regions",
          "Render",
          "Render's account",
          "Builds, backups, point-in-time recovery, TLS",
        ),
        ocel: ocelAccountHosted,
        verdict: "The database and its backups are Render's to run, not yours.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "yaml",
            filename: "render.yaml",
            code: `services:
  - name: my-web-app
    type: web
    runtime: node
    buildCommand: npm install
    startCommand: npm start
    envVars:
      - key: DATABASE_URL
        fromDatabase:
          name: my-database
          property: connectionString

databases:
  - name: my-database
    plan: 0.5c-1g`,
          },
        },
        ocel: appPostgres,
        verdict:
          "The Blueprint declares the database and re-resolves `fromDatabase` into the service on every sync; Ocel does the same wiring from the call site, so there is nothing to name twice.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "yaml",
            filename: "render.yaml",
            code: `services:
  - name: my-web-app
    type: web
    runtime: node
    plan: 0.5c-512mb
    buildCommand: npm install
    startCommand: npm start`,
          },
        },
        ocel: configAws,
        verdict:
          "Creating the deployment is a dashboard action — commit the Blueprint, then New > Blueprint — while " +
          ocelDeployCmd +
          " is a command, and its provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "There is no first-party command that runs local code against Render resources.",
            "The closest documented path: take the database's external connection URL from its Connect menu and put it in your own `.env`.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want one declarative file that provisions services and their databases together and keeps the connection string wired.",
            "You want backups, point-in-time recovery and storage autoscaling to be someone else's job.",
            "You want infrastructure reviewed in pull requests without owning a cloud account and its IAM.",
          ],
        },
        ocel: { list: [ocelPickOwnAccount, ocelPickOneLine, ocelPickAppCode] },
        verdict: "",
      },
    ],
  },
  {
    slug: "fly",
    name: "Fly.io",
    family: "hosted",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Fly's infrastructure, as Machines in Fly's datacenters",
          "Fly",
          "Fly's account, as Managed Postgres",
          "Anycast routing, the Machines lifecycle, Postgres operations",
        ),
        ocel: ocelAccountHosted,
        verdict: "The account holding the app and the database is Fly's.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "bash",
            code: `fly mpg create
fly mpg attach <CLUSTER_ID> --app my-node-app`,
          },
        },
        ocel: appPostgres,
        verdict:
          "`fly mpg attach` sets a `DATABASE_URL` secret on the app and nothing goes in `fly.toml`; in Ocel the database is named where it is used and there is no attach step.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "toml",
            filename: "fly.toml",
            code: `app = "my-node-app"
primary_region = "ord"

[build]
  dockerfile = "Dockerfile"

[http_service]
  internal_port = 8080
  force_https = true

[[vm]]
  size = "shared-cpu-1x"
  memory = "512mb"`,
          },
        },
        ocel: configAws,
        verdict:
          "`primary_region` and `[[vm]]` select among Fly's own regions and Machine sizes, so the image is portable but the file is not; the Ocel config's provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "There is no first-party command that runs local code with a Fly app's secrets injected.",
            "The closest documented path: `fly proxy` forwards a Fly service to localhost, and the Managed Postgres connection string can go in a local `.env`.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You need the app itself running close to users in many regions, out of one config file.",
            "Your workload is long-running, stateful or not HTTP-shaped: persistent connections, background workers, volumes, GPUs.",
            "You already have a Dockerfile and want scale-to-zero and fast boots without provisioning VMs, load balancers and networking yourself.",
          ],
        },
        ocel: { list: [ocelPickOwnAccount, ocelPickOneLine, ocelPickAppCode] },
        verdict: "",
      },
    ],
  },
  {
    slug: "terraform",
    name: "Terraform",
    family: "iac",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "The AWS account behind your provider credentials",
          "AWS",
          "Your account, as an RDS instance",
          "Nothing; state is local or in a backend you configure",
        ),
        ocel: ocelAccountCloud,
        verdict:
          "Both create everything under your own AWS credentials, with no vendor account in between.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "hcl",
            filename: "main.tf",
            code: `provider "aws" {
  region = "us-west-2"
}

resource "aws_db_instance" "default" {
  allocated_storage   = 10
  db_name             = "mydb"
  engine              = "postgres"
  instance_class      = "db.t3.micro"
  username            = "foo"
  password            = "foobarbaz"
  skip_final_snapshot = true
}`,
          },
        },
        ocel: appPostgres,
        verdict:
          "Terraform creates the instance and emits its connection attributes, and getting them into the app is a step you write elsewhere; the Ocel call is both the declaration and the client.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "hcl",
            filename: "main.tf",
            code: `provider "aws" {
  region = "us-west-2"
}

data "aws_ami" "ubuntu" {
  most_recent = true
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
  owners = ["099720109477"]
}

resource "aws_instance" "app_server" {
  ami           = data.aws_ami.ubuntu.id
  instance_type = "t2.micro"
}`,
          },
        },
        ocel: configAws,
        verdict:
          "`terraform apply` gets you a server, but the build, the artifact upload and the runtime wiring are not steps it performs, whereas " +
          ocelDeployCmd +
          " does them and changes target on one line.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "Terraform has no local development mode.",
            "Its commands — `init`, `plan`, `apply` — run against real infrastructure.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You need provider-neutral coverage of infrastructure primitives, with one workflow across many providers.",
            "Your team reviews infrastructure as a plan diff, with an approval between plan and apply.",
            "Infrastructure is owned by a platform team separate from app code, and HCL not being a programming language is the point.",
          ],
        },
        ocel: {
          list: [
            "You want the app built and shipped by the same command that provisions what it needs.",
            ocelPickAppCode,
            "There is no interop package for Terraform, so pick Ocel here only where it can own the whole deployment.",
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "pulumi",
    name: "Pulumi",
    family: "iac",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Your own AWS account, via `@pulumi/aws`",
          "AWS",
          "Your account, as an RDS instance",
          "State, in Pulumi Cloud by default; self-managed backends are opt-in",
        ),
        ocel: ocelAccountCloud,
        verdict:
          "The resources are yours in both; the split is the state, which Pulumi keeps in its own backend unless you move it.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "ts",
            filename: "index.ts",
            code: `import * as aws from "@pulumi/aws";

const db = new aws.rds.Instance("default", {
  allocatedStorage: 10,
  dbName: "mydb",
  engine: "postgres",
  instanceClass: aws.rds.InstanceType.T3_Micro,
  username: "foo",
  password: "foobarbaz",
  skipFinalSnapshot: true,
});

export const dbEndpoint = db.endpoint;`,
          },
        },
        ocel: appPostgres,
        verdict:
          "The program exports the endpoint and the registry warns that the username and password are stored in state as plain text; the Ocel call has no exported endpoint to route anywhere.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "ts",
            filename: "index.ts",
            code: `import * as aws from "@pulumi/aws";

const ubuntu = aws.ec2.getAmiOutput({
  mostRecent: true,
  owners: ["099720109477"],
  filters: [{ name: "name", values: ["ubuntu/images/*-24.04-amd64-server-*"] }],
});

const server = new aws.ec2.Instance("app-server", {
  ami: ubuntu.id,
  instanceType: "t3.micro",
});

export const publicIp = server.publicIp;`,
          },
        },
        ocel: configAws,
        verdict:
          "`pulumi new aws-typescript` scaffolds the program and `pulumi up` creates the compute, but the Next.js build and getting the artifact onto it are not steps it performs, whereas " +
          ocelDeployCmd +
          " does both and changes target on one line.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "There is no live-code development mode.",
            "`pulumi watch`, documented as experimental, watches the directory and continuously updates the stack's real resources — it redeploys rather than running your app locally.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "beside",
        question: QUESTIONS.beside,
        them: {
          code: {
            lang: "ts",
            filename: "index.ts",
            code: `import { link } from "@ocel/pulumi";

const orders = new aws.rds.Instance("orders", {
  engine: "postgres",
  password,
});

link.postgres("orders", {
  host: orders.address,
  port: orders.port,
  database: orders.dbName,
  username: orders.username,
  password,
});`,
          },
        },
        ocel: linkedPostgres,
        verdict:
          '`@ocel/pulumi` hands a database you already manage in Pulumi to the app, which reads it with `postgres("orders")` like any other.',
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want infrastructure in a real programming language, with your existing TypeScript tooling and tests applying to it.",
            "You want one program and one deployment graph spanning several providers.",
            "You want state, locking and history handled by a managed backend out of the box.",
          ],
        },
        ocel: {
          list: [
            "You want the app built and shipped by the same command that provisions what it needs.",
            ocelPickAppCode,
            "You can run both: `@ocel/pulumi` links a Pulumi-managed database into an Ocel app rather than replacing the program.",
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "sst",
    name: "SST",
    family: "iac",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Your own AWS account, with your credentials",
          "AWS",
          "Your account, as an RDS instance",
          "Nothing; state and function packages go to buckets in your account",
        ),
        ocel: ocelAccountCloud,
        verdict:
          "Both deploy under your AWS credentials and keep their state in your account; the difference is how much of the infrastructure you name.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "ts",
            filename: "sst.config.ts",
            code: `async run() {
  const vpc = new sst.aws.Vpc("MyVpc");
  const database = new sst.aws.Postgres("MyDatabase", { vpc });

  new sst.aws.Nextjs("MyWeb", { link: [database], vpc });
}`,
          },
        },
        ocel: appPostgres,
        verdict:
          "SST declares the database in the config and `link` puts its details on `Resource.MyDatabase` in app code; Ocel skips the declaration because the call in the app is it.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "ts",
            filename: "sst.config.ts",
            code: `export default $config({
  app(input) {
    return { name: "my-sst-app", home: "aws" };
  },
  async run() {
    new sst.aws.Nextjs("MyWeb");
  }
});`,
          },
        },
        ocel: configAws,
        verdict:
          "SST builds and deploys the Next.js app itself, so this is the closest comparison on the page; changing where it goes means changing components, while the Ocel config changes target on one line.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "`sst dev` deploys a stub Lambda and runs your function code on your machine, so real IAM applies and a permissions failure on AWS fails locally too.",
            "`sst.aws.Postgres` also takes a `dev` block pointing at a local Postgres.",
          ],
        },
        ocel: ocelDev,
        verdict:
          "Both run your code locally against real resources; once the project is linked on the console, `ocel dev` wraps your framework's own dev command rather than a function runtime.",
      },
      {
        key: "beside",
        question: QUESTIONS.beside,
        them: {
          code: {
            lang: "ts",
            filename: "sst.config.ts",
            code: `async run() {
  const { link } = await import("@ocel/sst");
  const vpc = new sst.aws.Vpc("Vpc");
  const orders = new sst.aws.Postgres("Orders", { vpc });
  link.postgres("orders", orders);
}`,
          },
        },
        ocel: linkedPostgres,
        verdict:
          '`@ocel/sst` hands a database you already manage in SST to the app, which reads it with `postgres("orders")` like any other.',
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want the framework build, the infrastructure and the wiring to be one deploy, with components for the AWS pieces around them.",
            "You want the live-Lambda loop: iterate on function code locally with real IAM and real deployed resources behind it.",
            "You expect to reach past the components into the wider Pulumi and Terraform provider set.",
          ],
        },
        ocel: {
          list: [
            "You want one config that names a target, with AWS and a VPS both reachable from it.",
            ocelPickAppCode,
            "You can run both: `@ocel/sst` links an SST-managed database into an Ocel app rather than replacing the config.",
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "coolify",
    name: "Coolify",
    family: "selfhost",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Your own server, connected over SSH",
          "Your host",
          "That server, as a Docker container",
          "The control panel, self-hosted or run by Coolify",
        ),
        ocel: ocelAccountServer,
        verdict:
          "The apps run on your machines either way; Coolify adds a panel that has to be running for the workflow to work.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          list: [
            "New Resource → PostgreSQL in the Coolify UI; there is no file in your repo.",
            "Coolify gives the database an internal URL, and you paste it into the application's environment variables yourself.",
          ],
        },
        ocel: appPostgres,
        verdict:
          "The connection string is copied by hand between two screens; in Ocel the name in the app code is the only place the database is written down.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          list: [
            "Add a server (SSH key, Docker Engine 24+), then New Resource → Application in the UI.",
            "Choose a source and a build pack, set Port Exposes, and click Deploy.",
            "With a GitHub App source, pushes to the branch deploy automatically.",
          ],
        },
        ocel: configVps,
        verdict:
          "The deployment is described in the panel's database rather than your repository, while the Ocel config is a file you review and its provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "There is no local-development integration in the docs.",
            "To reach the database from your machine you set it accessible over the internet and connect to the public URL.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want a UI over a plain VPS: databases, services, backups, SSL and environment variables without deploy config in the repo.",
            "You are consolidating many small apps and databases onto servers you already rent and want one dashboard over them.",
            "You want the panel itself to be open source, with the option to have it hosted for you.",
          ],
        },
        ocel: {
          list: [
            "You want the deployment described by a file in the repo, with no panel to run and keep up to date.",
            ocelPickOneLine,
            ocelPickAppCode,
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "kamal",
    name: "Kamal",
    family: "selfhost",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Your own servers, listed by IP",
          "Your host",
          "A host you name, in an accessory container",
          "Nothing; Kamal runs from your machine over SSH",
        ),
        ocel: ocelAccountServer,
        verdict:
          "Neither puts anything between you and the machine; what differs is how much of the machine you describe.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "yaml",
            filename: "config/deploy.yml",
            code: `accessories:
  db:
    image: postgres:16
    host: 192.168.0.2
    port: "127.0.0.1:5432:5432"
    env:
      clear:
        POSTGRES_USER: app
      secret:
        - POSTGRES_PASSWORD
    directories:
      - data:/var/lib/postgresql/data`,
          },
        },
        ocel: appPostgres,
        verdict:
          "The accessory is booted once with `kamal accessory boot db` and is not updated when you deploy; the Ocel database is created and kept current by the deploy that reads the call.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "yaml",
            filename: "config/deploy.yml",
            code: `service: hey
image: 37s/hey
servers:
  - 192.168.0.1
  - 192.168.0.2
registry:
  username: registry-user-name
  password:
    - KAMAL_REGISTRY_PASSWORD
proxy:
  host: hey.example.com
  app_port: 3000
  ssl: true
env:
  clear:
    DATABASE_HOST: 192.168.0.2
  secret:
    - DATABASE_PASSWORD`,
          },
        },
        ocel: configVps,
        verdict:
          "Kamal names the servers, the registry and every variable the container needs, while the Ocel config names a target and its provider line is the only thing that changes when that target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "Kamal is a deploy tool; the docs cover no local development mode.",
            "The accessory Postgres is bound to `127.0.0.1` on its host, so reaching it from your machine is a tunnel you set up.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want the whole deploy in one file in the repo, with no control-plane daemon, dashboard or vendor account in the loop.",
            "You need gapless deploys and automatic Let's Encrypt HTTPS on plain VMs, without adopting Kubernetes.",
            "Your app is already containerized and the database is external, or one stateful box you accept managing.",
          ],
        },
        ocel: {
          list: [
            "You want the database provisioned and kept current by the same deploy as the app.",
            ocelPickOneLine,
            ocelPickAppCode,
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "dokku",
    name: "Dokku",
    family: "selfhost",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Your own Linux host running Dokku",
          "Your host",
          "That host, in a plugin-managed container",
          "Nothing; Dokku is installed on your host",
        ),
        ocel: ocelAccountServer,
        verdict: "Both stay on one machine you rent, with nothing running anywhere else.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "bash",
            code: `dokku plugin:install https://github.com/dokku/dokku-postgres.git --name postgres
dokku postgres:create lollipop
dokku postgres:link lollipop playground`,
          },
        },
        ocel: appPostgres,
        verdict:
          "`postgres:link` sets `DATABASE_URL` on the app from a shell on the Dokku host; the Ocel database is named in the app code and created from there.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "bash",
            code: `dokku apps:create my-app

git remote add dokku dokku@dokku.me:my-app
git push dokku main`,
          },
        },
        ocel: configVps,
        verdict:
          "Dokku detects the buildpack so the repo needs no config at all, and the host is the git remote; the Ocel config is the one file, and its provider line is the only thing that changes when the target does.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "Nothing local by default: the database listens only on Dokku's internal Docker network.",
            "`dokku postgres:expose lollipop 5432` opens it to your machine, and `postgres:unexpose` closes it again.",
          ],
        },
        ocel: ocelDev,
        verdict: ocelDevVerdict,
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "You want a git-push workflow on one server you own, with buildpack detection so app repos need no deploy config.",
            "You want `postgres:create` and `postgres:link` to wire `DATABASE_URL` for you, with the same shape for Redis and the rest.",
            "You are happy administering one host over SSH and your scale fits one host.",
          ],
        },
        ocel: {
          list: [
            "You want the same workflow to reach AWS as well as a server, from one config.",
            ocelPickAppCode,
            "You would rather not run commands on the host to create and link a database.",
          ],
        },
        verdict: "",
      },
    ],
  },
  {
    slug: "compose",
    name: "Docker Compose",
    family: "selfhost",
    rows: [
      {
        key: "account",
        question: QUESTIONS.account,
        them: themAccount(
          "Whatever machine you run `docker compose` on",
          "Your host",
          "That machine, in a named volume",
          "Nothing",
        ),
        ocel: ocelAccountServer,
        verdict:
          "Neither has a vendor in the loop; Compose also has no opinion about how the file reaches a server.",
      },
      {
        key: "database",
        question: QUESTIONS.database,
        them: {
          code: {
            lang: "yaml",
            filename: "compose.yaml",
            code: `services:
  db:
    image: postgres:18
    environment:
      POSTGRES_PASSWORD: mysecretpassword
    volumes:
      - postgres_data:/var/lib/postgresql
    ports:
      - "127.0.0.1:5432:5432"

volumes:
  postgres_data:`,
          },
        },
        ocel: appPostgres,
        verdict:
          "Compose runs the database as another container with its data in a volume you back up; Ocel's call provisions it on the target and hands back a client.",
      },
      {
        key: "deploy",
        question: QUESTIONS.deploy,
        them: {
          code: {
            lang: "yaml",
            filename: "compose.yaml",
            code: `services:
  db:
    image: postgres:18
    environment:
      POSTGRES_PASSWORD: mysecretpassword
    volumes:
      - postgres_data:/var/lib/postgresql

  app:
    build: ./my-app
    environment:
      DATABASE_URL: postgresql://postgres:mysecretpassword@db:5432/mydb
    depends_on:
      - db

volumes:
  postgres_data:`,
          },
        },
        ocel: configVps,
        verdict:
          "There is no remote deploy in Compose, so getting the file and the images onto a host is yours to arrange, while " +
          ocelDeployCmd +
          " does it and changes target on one line.",
      },
      {
        key: "dev",
        question: QUESTIONS.dev,
        them: {
          list: [
            "The same `compose.yaml` runs on your laptop, so local dev is a real Postgres container.",
            "`docker compose up --watch` syncs or rebuilds on file change.",
          ],
        },
        ocel: ocelDev,
        verdict:
          "Compose gives you a local database; once the project is linked on the console, `ocel dev` gives you the deployed one, resolved for every machine on the team.",
      },
      {
        key: "pick",
        question: QUESTIONS.pick,
        them: {
          list: [
            "The app and its database are one unit you want in a single portable file that runs the same on a laptop, in CI and on one server.",
            "You want nothing on top of Docker: no control plane, no agent, nothing to upgrade but Docker itself.",
            "Your deployment is one host and you already have your own answers for TLS, rollout, secrets and backups.",
          ],
        },
        ocel: {
          list: [
            "You want TLS, rollout and secrets handled by the deploy rather than assembled around it.",
            ocelPickOneLine,
            ocelPickAppCode,
          ],
        },
        verdict: "",
      },
    ],
  },
];

export function find(slug: string | null | undefined): Tool {
  return (
    tools.find((tool) => tool.slug === slug) ??
    (tools.find((tool) => tool.slug === DEFAULT) as Tool)
  );
}
