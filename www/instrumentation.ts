import { SeverityNumber } from "@opentelemetry/api-logs";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-http";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { BatchLogRecordProcessor, LoggerProvider } from "@opentelemetry/sdk-logs";

const projectToken = process.env.NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN;
const apiHost = process.env.NEXT_PUBLIC_POSTHOG_HOST;

function createLoggerProvider() {
  if (!projectToken || !apiHost) return undefined;

  return new LoggerProvider({
    resource: resourceFromAttributes({ "service.name": "ocel-www" }),
    processors: [
      new BatchLogRecordProcessor({
        exporter: new OTLPLogExporter({
          url: new URL("/i/v1/logs", apiHost).toString(),
          headers: {
            Authorization: `Bearer ${projectToken}`,
            "Content-Type": "application/json",
          },
        }),
      }),
    ],
  });
}

export const loggerProvider = createLoggerProvider();

// This logger is intentionally not registered globally so only log records added by this integration export.
export const posthogLogsLogger = loggerProvider?.getLogger("ocel.posthog");

export { SeverityNumber };
