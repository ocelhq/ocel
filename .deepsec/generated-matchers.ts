import { compileDeclarativeMatchers, type DeepsecPlugin } from "deepsec/config";

const specs = [
  {
    "version": 1,
    "slug": "better-auth-next-registration",
    "description": "Better Auth initialization, device authorization, and Next.js authentication endpoint bindings.",
    "noiseTier": "precise",
    "filePatterns": [
      "console/auth/src/*.ts",
      "console/api/src/routes/auth/route.ts",
      "console/web/app/api/auth/**/route.ts"
    ],
    "patterns": [
      {
        "source": "^\\s*export\\s+const\\s+auth\\s*=\\s*betterAuth\\s*\\(",
        "flags": "m",
        "label": "Better Auth instance"
      },
      {
        "source": "^\\s*deviceAuthorization\\s*\\(\\s*\\{",
        "flags": "m",
        "label": "Device authorization plugin"
      },
      {
        "source": "^\\s*return\\s+auth\\.handler\\s*\\(\\s*request\\s*\\)\\s*;?",
        "flags": "m",
        "label": "Better Auth request handler"
      },
      {
        "source": "^\\s*export\\s+const\\s*\\{\\s*GET\\s*,\\s*POST\\s*\\}\\s*=\\s*toNextJsHandler\\s*\\(\\s*auth\\s*\\)\\s*;?",
        "flags": "m",
        "label": "Next.js authentication route"
      }
    ],
    "examples": [
      "export const auth = betterAuth(authConfig);",
      "deviceAuthorization({",
      "return auth.handler(request);",
      "export const { GET, POST } = toNextJsHandler(auth);"
    ],
    "closesSurfaceIds": [
      "console-auth-http"
    ]
  },
  {
    "version": 1,
    "slug": "next-control-route-binding",
    "description": "Repository-specific control API handlers and their Next.js method bindings.",
    "noiseTier": "precise",
    "filePatterns": [
      "console/web/app/api/projects/**/route.ts",
      "console/web/app/api/resources/**/route.ts",
      "console/web/app/api/blob/**/route.ts",
      "console/api/src/routes/**/route.ts"
    ],
    "patterns": [
      {
        "source": "^\\s*export\\s*\\{[^}\\n]+\\bas\\s+(?:GET|POST)\\b[^}\\n]*\\}\\s*from\\s*[\"']@console/api[\"']\\s*;?",
        "flags": "m",
        "label": "Next.js control route binding"
      },
      {
        "source": "^\\s*export\\s+async\\s+function\\s+(?:GET|POST)\\s*\\(",
        "flags": "m",
        "label": "Next.js control route method"
      },
      {
        "source": "^\\s*export\\s+(?:async\\s+)?function\\s+(?:listProjects|createProject|getProjectById|resolveResources|detectUploads|presignUpload|uploadStatus|verifyUploadSignature)\\s*\\(",
        "flags": "m",
        "label": "Control API handler implementation"
      }
    ],
    "examples": [
      "export { createProject as POST, listProjects as GET } from \"@console/api\";",
      "export async function GET(",
      "export async function resolveResources(request: Request): Promise<Response> {"
    ],
    "closesSurfaceIds": [
      "console-control-api"
    ]
  },
  {
    "version": 1,
    "slug": "aws-iam-function-url-registration",
    "description": "AWS IAM Function URL provisioning and the runtime entrypoints serving its invocations.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/membrane/src/**/entrypoint.mts",
      "platform/aws/membrane/src/node/fetch-bridge.mts",
      "platform/aws/membrane/src/shared/membrane.mts",
      "platform/aws/functions/image-optimizer/src/*.mts",
      "platform/aws/provider/cmd/membrane/bootstrap/*.go",
      "platform/aws/provider/deploy/function.go"
    ],
    "patterns": [
      {
        "source": "^\\s*await\\s+serveInvoke\\s*\\(",
        "flags": "m",
        "label": "Function invocation server"
      },
      {
        "source": "^\\s*export\\s+const\\s+handler\\s*=\\s*buildHandler\\s*\\(\\s*\\)\\s*;?",
        "flags": "m",
        "label": "Streaming Lambda handler"
      },
      {
        "source": "^\\s*url\\s*,\\s*err\\s*:=\\s*lambda\\.NewFunctionUrl\\s*\\(",
        "flags": "m",
        "label": "Function URL resource"
      },
      {
        "source": "^\\s*AuthorizationType:\\s*pulumi\\.String\\s*\\(\\s*functionURLAuthIAM\\s*\\)",
        "flags": "m",
        "label": "IAM Function URL authorization"
      },
      {
        "source": "^\\s*rt\\s*:=\\s*newRuntimeClient\\s*\\(\\s*os\\.Getenv\\s*\\(\\s*\"AWS_LAMBDA_RUNTIME_API\"\\s*\\)\\s*\\)",
        "flags": "m",
        "label": "Lambda custom runtime client"
      }
    ],
    "excludeFilePatterns": [
      "**/*_test.go"
    ],
    "examples": [
      "await serveInvoke(invoke);",
      "export const handler = buildHandler();",
      "url, err := lambda.NewFunctionUrl(ctx, resourceName, &lambda.FunctionUrlArgs{",
      "AuthorizationType: pulumi.String(functionURLAuthIAM),",
      "rt := newRuntimeClient(os.Getenv(\"AWS_LAMBDA_RUNTIME_API\"))"
    ],
    "closesSurfaceIds": [
      "aws-function-url-origins"
    ]
  },
  {
    "version": 1,
    "slug": "ocel-blob-route-adapter",
    "description": "Ocel blob HTTP route factories for the core, Next.js, Hono, and Express adapters.",
    "noiseTier": "precise",
    "filePatterns": [
      "packages/ocel/src/blob/route.ts",
      "packages/ocel/src/blob/next.ts",
      "packages/ocel/src/blob/hono.ts",
      "packages/ocel/src/blob/express.ts"
    ],
    "patterns": [
      {
        "source": "^\\s*export\\s+function\\s+createRouteHandler\\s*\\(",
        "flags": "m",
        "label": "Blob route factory"
      },
      {
        "source": "^\\s*const\\s+\\{\\s*GET\\s*,\\s*POST\\s*\\}\\s*=\\s*coreCreateRouteHandler\\s*\\(",
        "flags": "m",
        "label": "Blob adapter method binding"
      }
    ],
    "examples": [
      "export function createRouteHandler(",
      "const { GET, POST } = coreCreateRouteHandler(bucket, options);"
    ],
    "closesSurfaceIds": [
      "application-blob-http"
    ]
  },
  {
    "version": 1,
    "slug": "signed-upload-callback-dispatch",
    "description": "Signed upload-completion callback routing and delivery primitives.",
    "noiseTier": "precise",
    "filePatterns": [
      "packages/ocel/src/blob/route.ts",
      "cli/internal/devserver/detector.go",
      "platform/aws/provider/membrane/bucket/listener.go",
      "console/api/src/routes/blob/signing.ts"
    ],
    "patterns": [
      {
        "source": "^\\s*if\\s*\\(\\s*op\\s*===\\s*[\"']callback[\"']\\s*\\)\\s*return\\s+handleCallback\\s*\\(",
        "flags": "m",
        "label": "Upload callback route dispatch"
      },
      {
        "source": "^\\s*func\\s+\\([^)]*\\)\\s+postCallback\\s*\\(",
        "flags": "m",
        "label": "Upload callback delivery"
      }
    ],
    "examples": [
      "if (op === \"callback\") return handleCallback(bucket, getCtx(), req);",
      "func (d *detector) postCallback(ctx context.Context, c completion) error {",
      "func (l *Listener) postCallback(ctx context.Context, sess session, f sessionFile) error {"
    ],
    "closesSurfaceIds": [
      "blob-completion-webhook"
    ]
  },
  {
    "version": 1,
    "slug": "ocel-connect-session-service",
    "description": "Session-authenticated Connect RPC service registrations and generated handler implementations.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/provider/server/*.go",
      "platform/aws/provider/membrane/mux.go",
      "platform/aws/provider/membrane/bucket/service.go",
      "platform/aws/provider/cmd/deploy/main.go",
      "platform/aws/provider/cmd/membrane/bootstrap/membraneserver.go",
      "platform/aws/provider/channelauth/interceptor.go",
      "pkg/channel/channel.go"
    ],
    "patterns": [
      {
        "source": "^\\s*path\\s*,\\s*handler\\s*(?::=|=)\\s*\\w+v1connect\\.New(?:Deployment|EnvVars|Bucket)ServiceHandler\\s*\\(",
        "flags": "m",
        "label": "Connect service registration"
      },
      {
        "source": "^\\s*func\\s+Interceptor\\s*\\(\\s*token\\s+string\\s*\\)\\s+connect\\.Interceptor",
        "flags": "m",
        "label": "Connect session interceptor"
      },
      {
        "source": "^\\s*httpSrv\\s*:=\\s*&http\\.Server\\s*\\{\\s*Handler:\\s*(?:server|membrane)\\.NewMux\\s*\\(\\s*token\\b",
        "flags": "m",
        "label": "Authenticated RPC server"
      },
      {
        "source": "^\\s*func\\s+\\(s\\s+\\*(?:Server|VarsServer|Service)\\)\\s+\\w+\\s*\\([^\\n]*\\breq\\s+\\*(?:deploymentsv1|envv1|bucketsv1)\\.\\w+Request\\b",
        "flags": "m",
        "label": "Generated Connect handler implementation"
      }
    ],
    "excludeFilePatterns": [
      "**/*_test.go"
    ],
    "examples": [
      "path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&Server{}, interceptors)",
      "func Interceptor(token string) connect.Interceptor {",
      "httpSrv := &http.Server{Handler: server.NewMux(token)}",
      "func (s *Server) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {"
    ],
    "closesSurfaceIds": [
      "provider-session-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "loopback-development-connect-service",
    "description": "Loopback development Connect services, sync handler, and server binding.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/internal/cmd/devserver/main.go",
      "cli/internal/devserver/*.go",
      "packages/ocel/src/utils/rpc.ts"
    ],
    "patterns": [
      {
        "source": "^\\s*\\w+Path\\s*,\\s*\\w+Handler\\s*:=\\s*\\w+v1connect\\.New(?:Resource|Dev|Bucket)ServiceHandler\\s*\\(",
        "flags": "m",
        "label": "Development Connect service registration"
      },
      {
        "source": "^\\s*mux\\.HandleFunc\\s*\\(\\s*\"/sync\"\\s*,",
        "flags": "m",
        "label": "Development sync endpoint"
      },
      {
        "source": "^\\s*httpSrv\\s*:=\\s*&http\\.Server\\s*\\{\\s*Handler:\\s*srv\\.Mux\\s*\\(\\s*\\)\\s*\\}",
        "flags": "m",
        "label": "Loopback development server"
      },
      {
        "source": "^\\s*func\\s+\\(s\\s+\\*(?:Server|runtimeShim)\\)\\s+\\w+\\s*\\([^\\n]*(?:req|_)\\s+\\*(?:resourcesv1|devv1|bucketsv1)\\.\\w+Request\\b",
        "flags": "m",
        "label": "Development RPC handler implementation"
      }
    ],
    "excludeFilePatterns": [
      "**/*_test.go"
    ],
    "examples": [
      "resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s)",
      "mux.HandleFunc(\"/sync\", s.handleSync)",
      "httpSrv := &http.Server{Handler: srv.Mux()}",
      "func (s *runtimeShim) PresignUpload(ctx context.Context, req *bucketsv1.PresignUploadRequest) (*bucketsv1.PresignUploadResponse, error) {"
    ],
    "closesSurfaceIds": [
      "local-development-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "ocel-cobra-registration",
    "description": "Ocel Cobra command declarations, root registrations, and executable entrypoint.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/ocel/main.go",
      "cli/internal/cli/*.go",
      "cli/internal/authclient/*.go",
      "cli/internal/credentials/credentials.go"
    ],
    "patterns": [
      {
        "source": "^\\s*var\\s+\\w+Cmd\\s*=\\s*&cobra\\.Command\\s*\\{",
        "flags": "m",
        "label": "Cobra command declaration"
      },
      {
        "source": "^\\s*rootCmd\\.AddCommand\\s*\\(",
        "flags": "m",
        "label": "Cobra root command registration"
      },
      {
        "source": "^\\s*if\\s+err\\s*:=\\s*cli\\.Execute\\s*\\(\\s*\\)",
        "flags": "m",
        "label": "Ocel CLI entrypoint"
      }
    ],
    "excludeFilePatterns": [
      "**/*_test.go"
    ],
    "examples": [
      "var deployCmd = &cobra.Command{",
      "rootCmd.AddCommand(deployCmd)",
      "if err := cli.Execute(); err != nil {"
    ],
    "closesSurfaceIds": [
      "ocel-cli"
    ]
  },
  {
    "version": 1,
    "slug": "aws-sqs-revalidation-consumer",
    "description": "AWS Lambda SQS batch handler for revalidation messages.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/functions/revalidator/src/*.mts"
    ],
    "patterns": [
      {
        "source": "^\\s*export\\s+const\\s+handler\\s*=\\s*async\\s*\\(\\s*event\\s*:\\s*\\{\\s*Records\\?\\s*:\\s*SqsRecord\\[\\]\\s*\\}\\s*\\)",
        "flags": "m",
        "label": "SQS Lambda handler"
      },
      {
        "source": "^\\s*const\\s+parsed\\s*=\\s*parseMessage\\s*\\(\\s*record\\.body\\s*\\)\\s*;?",
        "flags": "m",
        "label": "Revalidation message consumer"
      }
    ],
    "examples": [
      "export const handler = async (event: { Records?: SqsRecord[] }): Promise<BatchResponse> =>",
      "const parsed = parseMessage(record.body);"
    ],
    "closesSurfaceIds": [
      "sqs-revalidation"
    ]
  },
  {
    "version": 1,
    "slug": "aws-dynamodb-tag-stream-consumer",
    "description": "AWS Lambda DynamoDB stream batch handler for ISR tag publication.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/functions/tag-publisher/src/*.mts"
    ],
    "patterns": [
      {
        "source": "^\\s*export\\s+const\\s+handler\\s*=\\s*async\\s*\\(\\s*event\\s*:\\s*\\{\\s*Records\\?\\s*:\\s*StreamRecord\\[\\]\\s*\\}\\s*\\)",
        "flags": "m",
        "label": "DynamoDB stream Lambda handler"
      },
      {
        "source": "^\\s*const\\s+raises\\s*=\\s*raisesOf\\s*\\(\\s*event\\.Records\\s*\\?\\?\\s*\\[\\]\\s*\\)\\s*;?",
        "flags": "m",
        "label": "DynamoDB stream record dispatch"
      }
    ],
    "examples": [
      "export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {",
      "const raises = raisesOf(event.Records ?? []);"
    ],
    "closesSurfaceIds": [
      "dynamodb-tag-publisher"
    ]
  },
  {
    "version": 1,
    "slug": "aws-function-url-runtime-bridge",
    "description": "Ocel AWS Function URL event dispatch into Node and Next.js origin runtimes.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/membrane/src/node/entrypoint.mts",
      "platform/aws/membrane/src/node/fetch-bridge.mts",
      "platform/aws/membrane/src/next/entrypoint.mts",
      "platform/aws/membrane/src/shared/membrane.mts",
      "platform/aws/provider/cmd/membrane/bootstrap/*.go",
      "platform/aws/provider/deploy/function.go"
    ],
    "patterns": [
      {
        "source": "serveInvoke\\(",
        "label": "Node origin invocation server"
      },
      {
        "source": "AuthorizationType: pulumi\\.String\\(functionURLAuthIAM\\)",
        "label": "IAM-protected Function URL"
      },
      {
        "source": "func handleInvocation\\(",
        "label": "Lambda runtime invocation dispatch"
      },
      {
        "source": "type funcURLRequest struct",
        "label": "Function URL event contract"
      },
      {
        "source": "http\\.createServer\\(wrapWithOcelContext\\(invoke\\)\\)",
        "label": "Origin request listener"
      },
      {
        "source": "fetchToNodeHandler\\(",
        "label": "Fetch-to-Node handler bridge"
      }
    ],
    "examples": [
      "await serveInvoke(invokeFor(resolveHandler(loaded.value)));",
      "AuthorizationType: pulumi.String(functionURLAuthIAM),",
      "func handleInvocation(ctx context.Context, rt *runtimeClient, m *Membrane) error {",
      "type funcURLRequest struct {",
      "return startServer(http.createServer(wrapWithOcelContext(invoke)), onListening);",
      "return fetchToNodeHandler(resolved.fetch);"
    ],
    "closesSurfaceIds": [
      "aws-function-url-origins"
    ]
  },
  {
    "version": 1,
    "slug": "aws-image-optimizer-handler-pipeline",
    "description": "AWS streaming image-optimizer handler and its request-validation, upstream-fetch, transformation, and storage pipeline.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/functions/image-optimizer/src/*.mts"
    ],
    "patterns": [
      {
        "source": "export const handler = buildHandler\\(\\);",
        "label": "Image optimizer Lambda handler"
      },
      {
        "source": "lambda\\.streamifyResponse\\(",
        "label": "Streaming Lambda response handler"
      },
      {
        "source": "export (?:async )?function (?:isReachableAddress|loadImageConfig|releaseAssetPrefix|optimize|matchLocalPattern|matchRemotePattern|isAllowedRemote|detectContentType|s3Store|readCapped|transform|guardedLookup|fetchUpstream|validate)\\b",
        "label": "Image optimizer request pipeline primitive"
      },
      {
        "source": "export (?:class|const) (?:ImageError|BootstrapError|TooLargeError|IMAGE_PASSTHROUGH)\\b",
        "label": "Image optimizer boundary contract"
      }
    ],
    "examples": [
      "export const handler = buildHandler();",
      "return lambda.streamifyResponse(async (event, stream) => {",
      "export function isReachableAddress(address: string): boolean {",
      "export async function fetchUpstream(",
      "export class TooLargeError extends Error {",
      "export const IMAGE_PASSTHROUGH = \"x-ocel-image-passthrough\";"
    ],
    "closesSurfaceIds": [
      "aws-function-url-origins"
    ]
  },
  {
    "version": 1,
    "slug": "ocel-loopback-connect-dispatch",
    "description": "Loopback Connect RPC registration, clients, and development bucket-service dispatch.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/internal/cmd/devserver/main.go",
      "cli/internal/devserver/*.go",
      "packages/ocel/src/utils/rpc.ts"
    ],
    "patterns": [
      {
        "source": "(?:resourcesv1connect|devv1connect|bucketsv1connect)\\.New(?:Resource|Dev|Bucket)ServiceHandler\\(",
        "label": "Development Connect service registration"
      },
      {
        "source": "mux\\.HandleFunc\\(\"/sync\"",
        "label": "Development sync endpoint registration"
      },
      {
        "source": "createConnectTransport\\(",
        "label": "Development Connect client transport"
      },
      {
        "source": "(?:resourcesv1connect|devv1connect|bucketsv1connect)\\.New(?:Resource|Dev|Bucket)ServiceClient\\(",
        "label": "Development Connect service client"
      },
      {
        "source": "func \\(s \\*runtimeShim\\) (?:PresignUpload|VerifyUploadSignature|GetUploadStatus)\\(",
        "label": "Development bucket RPC implementation"
      }
    ],
    "examples": [
      "resourcePath, resourceHandler := resourcesv1connect.NewResourceServiceHandler(s)",
      "mux.HandleFunc(\"/sync\", s.handleSync)",
      "const transport = createConnectTransport({",
      "client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, url)",
      "func (s *runtimeShim) PresignUpload(ctx context.Context, req *bucketsv1.PresignUploadRequest) (*bucketsv1.PresignUploadResponse, error) {"
    ],
    "closesSurfaceIds": [
      "local-development-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "ocel-cli-command-execution",
    "description": "Execution entry points for Ocel CLI commands and their direct command-level tests.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/internal/cli/*.go"
    ],
    "patterns": [
      {
        "source": "\\brun(?:Bootstrap|Build|Deploy|DeploymentsLs|DeploymentsPrune|Destroy|DestroyPreviewProject|Dev|DomainUse|DomainLs|DomainRelease|EnvSet|EnvGet|EnvLs|EnvRm|EnvRef|EnvRefs|EnvHistory|Generate|Init|Link|Login|Logout|PreviewUp|PreviewRm|PreviewLs|PreviewPrune|Rollback|Run|Unlink)\\(",
        "label": "Ocel command execution entry point"
      }
    ],
    "examples": [
      "err := runDeploy(context.Background(), defaultDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(\"\"))",
      "if err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(\"\")); err != nil {",
      "err := runRollback(context.Background(), d, root, rollbackOptions{}, &stdout, &stderr)"
    ],
    "closesSurfaceIds": [
      "ocel-cli"
    ]
  },
  {
    "version": 1,
    "slug": "aws-sqs-revalidation-dispatch",
    "description": "SQS revalidation record dispatch through message validation and signed AWS origin requests.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/functions/revalidator/src/*.mts"
    ],
    "patterns": [
      {
        "source": "export const handler = async \\(event: \\{ Records\\?: SqsRecord\\[\\] \\}\\)",
        "label": "SQS revalidation Lambda handler"
      },
      {
        "source": "parseMessage\\(record\\.body\\)",
        "label": "Revalidation record validation"
      },
      {
        "source": "service: \"(?:s3|lambda)\"",
        "label": "Signed revalidation AWS request"
      },
      {
        "source": "batchItemFailures\\.push\\(",
        "label": "SQS partial-batch failure dispatch"
      }
    ],
    "examples": [
      "export const handler = async (event: { Records?: SqsRecord[] }): Promise<BatchResponse> =>",
      "const parsed = parseMessage(record.body);",
      "const client = new AwsClient({ ...deps.credentials, service: \"lambda\", region: target.region });",
      "batchItemFailures.push({ itemIdentifier: record.messageId });"
    ],
    "closesSurfaceIds": [
      "sqs-revalidation"
    ]
  },
  {
    "version": 1,
    "slug": "aws-dynamodb-tag-publication-dispatch",
    "description": "DynamoDB tag-stream dispatch through record merging, snapshot persistence, and authenticated edge publication.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/functions/tag-publisher/src/*.mts"
    ],
    "patterns": [
      {
        "source": "export const handler = async \\(event: \\{ Records\\?: StreamRecord\\[\\] \\}\\)",
        "label": "DynamoDB stream Lambda handler"
      },
      {
        "source": "export function raisesOf\\(",
        "label": "DynamoDB stream record dispatcher"
      },
      {
        "source": "export async function publishAll\\(",
        "label": "Tag snapshot batch publisher"
      },
      {
        "source": "export class S3TagSnapshotStore",
        "label": "Tag snapshot persistence adapter"
      },
      {
        "source": "export async function raise\\(",
        "label": "Authenticated ISR writer dispatch"
      },
      {
        "source": "export function config\\(ssm: SSMClient\\)",
        "label": "Tag publisher runtime configuration"
      }
    ],
    "examples": [
      "export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {",
      "export function raisesOf(records: readonly StreamRecord[]): Raises {",
      "export async function publishAll(",
      "export class S3TagSnapshotStore implements TagSnapshotStore {",
      "export async function raise(",
      "export function config(ssm: SSMClient): Promise<Config> {"
    ],
    "closesSurfaceIds": [
      "dynamodb-tag-publisher"
    ]
  },
  {
    "version": 1,
    "slug": "aws-connect-deploy-handler",
    "description": "AWS provider ConnectRPC handler method implementations (DeploymentService/EnvVarsService) plus the channel bearer-auth and traceparent interceptors that guard them.",
    "noiseTier": "normal",
    "filePatterns": [
      "platform/aws/provider/server/*.go",
      "platform/aws/provider/channelauth/*.go",
      "platform/aws/provider/tracecontext/*.go"
    ],
    "patterns": [
      {
        "source": "func \\(\\w+ \\*(Server|VarsServer)\\) [A-Z]\\w+\\(ctx context\\.Context, req \\*\\w+v1\\.",
        "label": "connectrpc-handler-method"
      },
      {
        "source": "func \\(\\w+ \\*interceptor\\) check\\(header http\\.Header\\) error",
        "label": "channel-bearer-check"
      },
      {
        "source": "channel\\.WithTraceParent\\(ctx,",
        "label": "traceparent-extract"
      }
    ],
    "excludeFilePatterns": [
      "**/*_test.go"
    ],
    "examples": [
      "func (s *Server) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) (err error) {",
      "func (s *VarsServer) SetValue(ctx context.Context, req *envv1.SetValueRequest) (*envv1.SetValueResponse, error) {",
      "func (a *interceptor) check(header http.Header) error {",
      "return channel.WithTraceParent(ctx, header.Get(channel.TraceParentHeader))"
    ],
    "closesSurfaceIds": [
      "aws-provider-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "aws-deploy-event-stream",
    "description": "The DeployEvent serialization path of the AWS provider streaming RPCs: the buffered event sender and tracer that relay provider logs, errors, and spans back to the CLI over the ConnectRPC stream.",
    "noiseTier": "normal",
    "filePatterns": [
      "platform/aws/provider/server/*.go"
    ],
    "patterns": [
      {
        "source": "newEventSender\\(",
        "label": "event-sender"
      },
      {
        "source": "DeployEvent_(Log|Result|Progress|Span|StagePlan)\\{",
        "label": "deploy-event-variant"
      },
      {
        "source": "func \\(t \\*eventTracer\\)",
        "label": "event-tracer-method"
      },
      {
        "source": "\\.Get(StagePlan|Span)\\(\\)",
        "label": "deploy-event-accessor"
      }
    ],
    "examples": [
      "sender := newEventSender(context.Background(), stream.send)",
      "Event: &deploymentsv1.DeployEvent_Log{Log: &deploymentsv1.LogEvent{Message: message}},",
      "func (t *eventTracer) Span(id, parentID deploy.StageID, name string, start, end time.Time, err error, attrs ...deploy.Attr) {",
      "first := stream.events[0].GetStagePlan()"
    ],
    "closesSurfaceIds": [
      "aws-provider-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "membrane-bucket-service-handler",
    "description": "Go membrane BucketService ConnectRPC handler implementations: presigned S3 PUT issuance, upload-signature verification, and upload-status reporting.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/provider/membrane/bucket/*.go"
    ],
    "patterns": [
      {
        "source": "func \\(s \\*Service\\) (PresignUpload|VerifyUploadSignature|GetUploadStatus)\\(ctx context\\.Context, req \\*bucketsv1\\.",
        "label": "bucket-service-handler-method"
      },
      {
        "source": "bucketsv1connect\\.BucketServiceHandler",
        "label": "bucket-service-handler-binding"
      }
    ],
    "examples": [
      "func (s *Service) PresignUpload(ctx context.Context, req *bucketsv1.PresignUploadRequest) (*bucketsv1.PresignUploadResponse, error) {",
      "func (s *Service) VerifyUploadSignature(ctx context.Context, req *bucketsv1.VerifyUploadSignatureRequest) (*bucketsv1.VerifyUploadSignatureResponse, error) {",
      "var _ bucketsv1connect.BucketServiceHandler = (*Service)(nil)"
    ],
    "closesSurfaceIds": [
      "membrane-bucket-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "membrane-upload-hmac",
    "description": "HMAC-SHA256 signing and constant-time verification of canonical upload payloads that authenticate bucket upload completions in the membrane.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/provider/membrane/bucket/*.go"
    ],
    "patterns": [
      {
        "source": "func verifyUpload\\(secret, sessionID string,",
        "label": "verify-upload-signature"
      },
      {
        "source": "hmac\\.Equal\\(\\[\\]byte\\(expected\\), \\[\\]byte\\(signature\\)\\)",
        "label": "constant-time-compare"
      },
      {
        "source": "signUpload\\(sess\\.Secret,",
        "label": "sign-completion-with-session-secret"
      }
    ],
    "examples": [
      "func verifyUpload(secret, sessionID string, file SignedFile, signature string) (bool, error) {",
      "return hmac.Equal([]byte(expected), []byte(signature)), nil",
      "signature, err := signUpload(sess.Secret, sess.SessionID, signed)"
    ],
    "closesSurfaceIds": [
      "membrane-bucket-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "membrane-upload-callback-listener",
    "description": "S3 object-created Lambda listener that resolves the upload session by object tag and posts an HMAC-signed completion to the session's callback origin, gated by an allow-list.",
    "noiseTier": "precise",
    "filePatterns": [
      "platform/aws/provider/membrane/bucket/*.go"
    ],
    "patterns": [
      {
        "source": "func \\(l \\*Listener\\) Handle\\(ctx context\\.Context, event S3Event\\)",
        "label": "s3-event-handler"
      },
      {
        "source": "func originAllowed\\(rawURL string, allowed \\[\\]string\\)",
        "label": "callback-origin-allowlist"
      },
      {
        "source": "signedCompletion\\{",
        "label": "signed-completion-callback"
      }
    ],
    "examples": [
      "func (l *Listener) Handle(ctx context.Context, event S3Event) error {",
      "func originAllowed(rawURL string, allowed []string) bool {",
      "body, err := json.Marshal(signedCompletion{"
    ],
    "closesSurfaceIds": [
      "membrane-bucket-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "membrane-upload-session-store",
    "description": "DynamoDB upload-session store for the membrane bucket service, including the conditional UpdateItem that guards the idempotent pending-to-succeeded file transition.",
    "noiseTier": "normal",
    "filePatterns": [
      "platform/aws/provider/membrane/bucket/*.go"
    ],
    "patterns": [
      {
        "source": "func \\(s \\*sessionStore\\) (put|get|markSucceeded)\\(ctx context\\.Context",
        "label": "session-store-op"
      },
      {
        "source": "ConditionExpression:\\s+aws\\.String\\(expr",
        "label": "conditional-transition-guard"
      }
    ],
    "examples": [
      "func (s *sessionStore) markSucceeded(ctx context.Context, sessionID string, idx int) (bool, error) {",
      "func (s *sessionStore) get(ctx context.Context, sessionID string) (session, error) {",
      "ConditionExpression:      aws.String(expr + \" = :pending\"),"
    ],
    "closesSurfaceIds": [
      "membrane-bucket-rpc"
    ]
  },
  {
    "version": 1,
    "slug": "cli-console-auth-client",
    "description": "The ocel CLI's console auth HTTP client: OAuth 2.0 device-authorization grant polling and bearer-token attachment against the console /api/auth endpoints.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/internal/authclient/*.go"
    ],
    "patterns": [
      {
        "source": "Header\\.Set\\(\"User-Agent\", \"ocel-cli\"\\)",
        "label": "cli-auth-request"
      },
      {
        "source": "oauth:grant-type:device_code",
        "label": "device-authorization-grant"
      },
      {
        "source": "Header\\.Set\\(\"Authorization\", \"Bearer \"\\s*\\+\\s*accessToken\\)",
        "label": "bearer-token-attach"
      }
    ],
    "examples": [
      "req.Header.Set(\"User-Agent\", \"ocel-cli\")",
      "\"grant_type\":  \"urn:ietf:params:oauth:grant-type:device_code\",",
      "req.Header.Set(\"Authorization\", \"Bearer \"+accessToken)"
    ],
    "closesSurfaceIds": [
      "ocel-cli"
    ]
  },
  {
    "version": 1,
    "slug": "cli-credential-store",
    "description": "The ocel CLI credential store: OS keyring with a 0600 file fallback and an OCEL_ACCESS_TOKEN environment override for the console access token.",
    "noiseTier": "precise",
    "filePatterns": [
      "cli/internal/credentials/*.go"
    ],
    "patterns": [
      {
        "source": "keyring\\.(Set|Get|Delete)\\(service, user",
        "label": "keyring-access"
      },
      {
        "source": "os\\.WriteFile\\(path, data, 0o600\\)",
        "label": "credential-file-fallback"
      },
      {
        "source": "os\\.Getenv\\(envAccessToken\\)",
        "label": "env-token-override"
      }
    ],
    "examples": [
      "if err := keyring.Set(service, user, string(data)); err == nil {",
      "if err := os.WriteFile(path, data, 0o600); err != nil {",
      "if token := os.Getenv(envAccessToken); token != \"\" {"
    ],
    "closesSurfaceIds": [
      "ocel-cli"
    ]
  }
];

export const generatedMatchersPlugin: DeepsecPlugin = {
  name: "deepsec-generated-matchers",
  matchers: compileDeclarativeMatchers(specs),
};
