export async function GET(
  _request: Request,
  { params }: { params: Promise<{ code: string }> },
) {
  const { code } = await params;
  return Response.json({ framework: "next" }, { status: Number(code) });
}
