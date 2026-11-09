import { Suspense } from "react";
import LiveGrid from "@/components/live-grid";

// Server Component. It renders no live data itself: everything that changes
// with the tick stream lives inside LiveGrid, which is the only client boundary.
export default function Page() {
  return (
    <Suspense fallback={<div style={{ padding: 40 }}><span className="lbl">loading</span></div>}>
      <LiveGrid
        httpBase={process.env.NEXT_PUBLIC_ENGINE_HTTP!}
        wsURL={process.env.NEXT_PUBLIC_ENGINE_WS!}
      />
    </Suspense>
  );
}
