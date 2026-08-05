// The golden comparison's probe (assert-suppression-golden.mjs). Its body must
// be byte-identical across two renders of the same deployment, so — unlike the
// /isr probe next door — nothing here may vary per render: no clock, no random,
// no request data. What the comparison is looking for is a difference the
// `purpose: prefetch` header caused, and a body that varies on its own can
// never show one.
//
// `revalidate` is what makes it a prerender at all, which is the class the
// suppression applies to, and it is short so the assertion can wait the entry
// into the STALE state — the only state where `purpose: prefetch` changes
// anything. Keep the marker and this number in sync with GOLDEN_MARKER and
// GOLDEN_REVALIDATE_SECONDS in ../../lib.mjs; lib.test.mjs asserts both.
export const revalidate = 3;

export default function Page() {
  return (
    <main>
      <p id="golden-body">golden-body:v1</p>
      <p>
        Rendered identically whether or not the edge asked for this page as a
        prefetch.
      </p>
    </main>
  );
}
