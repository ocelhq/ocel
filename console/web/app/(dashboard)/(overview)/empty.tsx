import { CommandPane } from "../command-pane";
import { noticeBody, PageNotice } from "../page-shell";

export function EmptyProjects() {
  return (
    <PageNotice heading="No projects yet">
      <p className={noticeBody}>
        Projects start from code. Run this in your app&rsquo;s directory to create one, or to link
        the app to a project that already exists.
      </p>
      <CommandPane command="ocel link" />
    </PageNotice>
  );
}
