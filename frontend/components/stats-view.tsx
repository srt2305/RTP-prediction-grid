"use client";

import { useEffect, useState } from "react";
import { ACCOUNT, getJSON } from "@/lib/api";
import { money, type Bucket, type Performance } from "@/lib/types";

const pct = (v: number, digits = 1) => `${(v * 100).toFixed(digits)}%`;

export default function StatsView() {
  const [perf, setPerf] = useState<Performance | null>(null);

  useEffect(() => {
    const load = () => void getJSON<Performance>(`/api/performance?account=${ACCOUNT}`).then(setPerf).catch(() => {});
    load();
    const id = setInterval(load, 4000);
    return () => clearInterval(id);
  }, []);

  if (!perf) return <div style={{ padding: 30 }}><span className="lbl">loading</span></div>;

  const s = perf.summary;
  const decided = s.hits + s.misses;
  const worst = Math.max(...perf.calibration.map((b) => Math.max(b.predicted, b.actual)), 0.01);

  return (
    <>
      <header className="top">
        <div className="topcell">
          <span className="lbl">Track record</span>
          <span style={{ fontSize: 15, fontWeight: 600 }}>{ACCOUNT}</span>
        </div>
        <div className="rule" />
        <Stat label="Net" value={money(s.netMinor)} tone={s.netMinor >= 0 ? "var(--pos)" : "var(--neg)"} />
        <Stat label="Staked" value={money(s.stakedMinor)} />
        <Stat label="Returned" value={money(s.returnedMinor)} />
        <div className="rule" />
        <Stat label="Hit rate" value={decided ? pct(s.hitRate) : "—"} />
        <Stat
          label="Per dollar"
          value={s.stakedMinor ? pct(s.expectancyPerDollar, 2) : "—"}
          tone={s.expectancyPerDollar >= 0 ? "var(--pos)" : "var(--neg)"}
        />
        <div style={{ flex: 1 }} />
        <div className="topcell" style={{ alignItems: "flex-end" }}>
          <span className="lbl">{s.positions} resolved positions</span>
          <span className="lbl">{s.hits} hit · {s.refunds} refunded · {s.misses} missed</span>
        </div>
      </header>

      <div className="sect">
        <span className="lbl">Model calibration</span>
        <span className="lbl">{perf.marketsAnalysed} settled markets replayed at sigma {perf.sigma}</span>
      </div>

      <div className="calwrap">
        <div className="calhead">
          <span className="lbl">Model probability</span>
          <span className="lbl r">Cells</span>
          <span className="lbl r">Predicted</span>
          <span className="lbl r">Actual</span>
          <span className="lbl">Predicted against actual</span>
          <span className="lbl r">Error</span>
        </div>
        {perf.calibration.map((b) => (
          <CalRow key={b.lower} b={b} worst={worst} />
        ))}
      </div>

      <div className="reading">
        <span className="lbl">Reading this</span>
        <p>
          Every settled market is replayed through the pricing model as it stood at open, then each
          cell is bucketed by the probability the model gave it. A calibrated model puts{" "}
          <b>actual</b> on top of <b>predicted</b> in every row.
        </p>
        <p>
          Where actual sits above predicted the model is too confident and the cell is underpriced —
          it pays more than it should. Where actual sits below, the cell is overpriced. Persistent
          divergence in the tails usually means the volatility input is wrong rather than the
          distribution being the wrong shape.
        </p>
      </div>
    </>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="topcell">
      <span className="lbl">{label}</span>
      <span className="spot" style={{ fontSize: 21, color: tone }}>{value}</span>
    </div>
  );
}

function CalRow({ b, worst }: { b: Bucket; worst: number }) {
  const error = b.actual - b.predicted;
  const bad = Math.abs(error) > Math.max(b.predicted * 0.5, 0.01);
  return (
    <div className="calrow">
      <span style={{ fontWeight: 500 }}>{pct(b.lower)} – {pct(b.upper)}</span>
      <span className="r" style={{ color: "var(--dim)" }}>{b.cells.toLocaleString()}</span>
      <span className="r" style={{ color: "var(--model)" }}>{pct(b.predicted, 2)}</span>
      <span className="r" style={{ color: bad ? "var(--neg)" : "var(--pos)" }}>{pct(b.actual, 2)}</span>
      <span className="bars">
        <i className="bar model" style={{ width: `${(b.predicted / worst) * 100}%` }} />
        <i className="bar actual" style={{ width: `${(b.actual / worst) * 100}%`,
             background: bad ? "var(--neg)" : "var(--pos)" }} />
      </span>
      <span className="r" style={{ color: bad ? "var(--neg)" : "var(--dim)" }}>
        {error >= 0 ? "+" : "−"}{pct(Math.abs(error), 2)}
      </span>
    </div>
  );
}
