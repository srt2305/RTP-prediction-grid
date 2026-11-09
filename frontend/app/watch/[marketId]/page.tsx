import WatchChart from "@/components/watch-chart";

// Server Component: it only unwraps the route params. The chart polls and
// animates, so it is the client boundary.
export default async function WatchPage({
  params,
  searchParams,
}: {
  params: Promise<{ marketId: string }>;
  searchParams: Promise<{ cell?: string; stake?: string }>;
}) {
  const { marketId } = await params;
  const { cell, stake } = await searchParams;

  return <WatchChart marketId={marketId} cell={Number(cell ?? 0)} stake={stake ?? ""} />;
}
