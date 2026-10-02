import { createFromSource } from "fumadocs-core/search/server";
import { after } from "next/server";
import { loggerProvider, posthogLogsLogger, SeverityNumber } from "@/instrumentation";
import { source } from "@/lib/source";

const { GET: search } = createFromSource(source, {
  // https://docs.orama.com/docs/orama-js/supported-languages
  language: "english",
});

function flushLogs() {
  after(async () => {
    await loggerProvider?.forceFlush();
  });
}

export async function GET(request: Request) {
  try {
    const response = await search(request);

    posthogLogsLogger?.emit({
      body: "Documentation search request completed",
      severityNumber: SeverityNumber.INFO,
      attributes: {
        operation: "documentation_search",
        outcome: "completed",
        route: "/api/search",
        status_code: response.status,
      },
    });
    flushLogs();

    return response;
  } catch (error) {
    posthogLogsLogger?.emit({
      body: "Documentation search request failed",
      severityNumber: SeverityNumber.ERROR,
      attributes: {
        error_type: "search_handler_error",
        operation: "documentation_search",
        outcome: "failed",
        route: "/api/search",
      },
    });
    flushLogs();

    throw error;
  }
}
