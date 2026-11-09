"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { getJSON, HTTP } from "@/lib/api";
import { countdown, money, price, type Watch, verdictOf } from "@/lib/types";

const W = 1000;
const H = 460;
const PAD = { top: 22, right: 104, bottom: 34, left: 66 };

const VERDICT = {
  hit: { title: "HIT", tone: "var(--pos)" },
  miss: { title: "MISSED", tone: "var(--neg)" },
  refunded: { title: "REFUNDED", tone: "var(--accent)" },
  void: { title: "VOID", tone: "var(--faint)" },
  pending: { title: "", tone: "var(--dim)" },
} as const;

export default function WatchChart({ marketId, cell, stake }: { marketId: string; cell: number; stake: string }) {
  const [watch, setWatch] = useState<Watch | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [revealed, setRevealed] = useState(false);

  useEffect(() => {
    let alive = true;
    const query = `/api/markets/${marketId}/watch?cell=${cell}${stake ? `&stake=${stake}` : ""}`;

    const poll = async () => {
      try {
        const next = await getJSON<Watch>(query);
        if (alive) setWatch(next);
      } catch {
        /* the market may not exist yet; the next tick retries */
      }
    };
    void poll();
    const id = setInterval(poll, 500);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [marketId, cell, stake]);

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 100);
    return () => clearInterval(id);
  }, []);

  const verdict = watch ? verdictOf(watch) : "pending";
  useEffect(() => {
    if (verdict !== "pending" && !revealed) {
      const id = setTimeout(() => setRevealed(true), 120);
      return () => clearTimeout(id);
    }
  }, [verdict, revealed]);

  const geometry = useMemo(() => {
    if (!watch) return null;

    const opens = new Date(watch.market.opensAt).getTime();
    const expires = new Date(watch.market.expiresAt).getTime();
    const span = Math.max(expires - opens, 1);

    const prices = watch.path.map((p) => p.price);
    const pad = watch.market.cellWidth * 0.75;
    // Tail cells are unbounded; clamping to the path keeps the chart readable
    // instead of squashing it against a zero or infinite edge.
    const bandLo = watch.lower ?? Math.min(...prices) - pad;
    const bandHi = watch.upper ?? Math.max(...prices) + pad;
    const lo = Math.min(bandLo, ...prices) - pad;
    const hi = Math.max(bandHi, ...prices) + pad;
    const range = Math.max(hi - lo, 1e-9);

    const x = (t: number) => PAD.left + ((t - opens) / span) * (W - PAD.left - PAD.right);
    const y = (p: number) => PAD.top + (1 - (p - lo) / range) * (H - PAD.top - PAD.bottom);

    const ticks = Array.from({ length: 5 }, (_, i) => lo + (range * i) / 4);
    return { opens, expires, x, y, lo, hi, ticks };
  }, [watch]);

  if (!watch || !geometry) {
    return <div style={{ padding: 40 }}><span className="lbl">loading the market</span></div>;
  }

  const { x, y } = geometry;
  const line = watch.path.map((p) => `${x(new Date(p.at).getTime()).toFixed(1)},${y(p.price).toFixed(1)}`).join(" ");
  const bandTop = y(watch.upper ?? geometry.hi);
  const bandBottom = y(watch.lower ?? geometry.lo);
  const settled = watch.market.settledPrice;
  const tone = VERDICT[verdict].tone;
  const bandTone = verdict === "hit" ? "var(--pos)" : verdict === "miss" ? "var(--neg)" : "var(--accent)";
  const multiple =
    watch.myPaidMinor > 0 && watch.myStakeMinor > 0 ? watch.myPaidMinor / watch.myStakeMinor : null;

  return (
    <div className="watch">
      <header className="top">
        <div className="topcell">
          <span className="lbl">Watching</span>
          <span style={{ fontSize: 15, fontWeight: 600 }}>{watch.market.symbol}</span>
        </div>
        <div className="rule" />
        <div className="topcell">
          <span className="lbl">Your band</span>
          <span className="spot" style={{ fontSize: 21 }}>
            {watch.lower === null ? "below" : price(watch.lower)} –{" "}
            {watch.upper === null ? "above" : price(watch.upper)}
          </span>
        </div>
        <div className="topcell">
          <span className="lbl">Spot</span>
          <span className="spot" style={{ fontSize: 21 }}>{price(watch.spot)}</span>
        </div>
        <div style={{ flex: 1 }} />
        {verdict === "pending" && (
          <>
            <div className="topcell" style={{ alignItems: "flex-end" }}>
              <span className="lbl">{now < new Date(watch.market.freezesAt).getTime() ? "Freezes in" : "Settles in"}</span>
              <span className="spot" style={{ fontSize: 21, color: "var(--accent)" }}>
                {countdown(now < new Date(watch.market.freezesAt).getTime() ? watch.market.freezesAt : watch.market.expiresAt, now)}
              </span>
            </div>
            <div className="rule" />
          </>
        )}
        <Link href="/" className="cta ghost" style={{ textDecoration: "none", lineHeight: "34px" }}>
          Back to grid
        </Link>
      </header>

      <div className="chartwrap">
        <div className="plot">
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="chart">
          {geometry.ticks.map((t) => (
            <line key={t} x1={PAD.left} y1={y(t)} x2={W - PAD.right} y2={y(t)}
                  stroke="var(--hair)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
          ))}

          <rect x={PAD.left} y={bandTop} width={W - PAD.left - PAD.right} height={Math.max(bandBottom - bandTop, 2)}
                fill={bandTone} opacity="0.16" />
          <rect x={PAD.left} y={bandTop} width={W - PAD.left - PAD.right} height={Math.max(bandBottom - bandTop, 2)}
                fill="none" stroke={bandTone} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
          <line x1={x(new Date(watch.market.freezesAt).getTime())} y1={PAD.top}
                x2={x(new Date(watch.market.freezesAt).getTime())} y2={H - PAD.bottom}
                stroke="var(--dim)" strokeWidth="1" strokeDasharray="4 4" vectorEffect="non-scaling-stroke" />
          <line x1={W - PAD.right} y1={PAD.top} x2={W - PAD.right} y2={H - PAD.bottom}
                stroke="var(--line)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />

          {line && (
            <polyline points={line} fill="none" stroke="var(--model)" strokeWidth="2.4"
                      vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" />
          )}

          {settled !== null && (
            <>
              <line x1={PAD.left} y1={y(settled)} x2={W - PAD.right} y2={y(settled)}
                    stroke={tone} strokeWidth="1.5" strokeDasharray="5 4" vectorEffect="non-scaling-stroke" />

            </>
          )}
        </svg>

        {geometry.ticks.map((t) => (
          <span key={t} className="ytick" style={{ top: `${(y(t) / H) * 100}%` }}>
            {price(t)}
          </span>
        ))}

        <span className="bandtag" style={{ top: `${(bandTop / H) * 100}%`, color: bandTone,
                                           borderColor: bandTone }}>
          your band {watch.lower === null ? "below" : price(watch.lower)} –{" "}
          {watch.upper === null ? "above" : price(watch.upper)}
        </span>

        {settled !== null && (
          <i className="dot-settled landed"
             style={{ top: `${(y(settled) / H) * 100}%`, left: `${((W - PAD.right) / W) * 100}%`,
                      background: tone }} />
        )}

        {watch.path.length > 0 && verdict === "pending" && (
          <i className="dot-live pulse"
             style={{ top: `${(y(watch.spot) / H) * 100}%`,
                      left: `${(x(new Date(watch.path[watch.path.length - 1].at).getTime()) / W) * 100}%` }} />
        )}

        {settled !== null && (
          <span className="settletag" style={{ top: `${(y(settled) / H) * 100}%`, color: tone }}>
            {price(settled)}
          </span>
        )}

        <div className="axis">
          <span className="lbl">open</span>
          <span className="lbl" style={{ color: "var(--accent)" }}>freeze</span>
          <span className="lbl">expiry</span>
        </div>
        </div>

        {verdict !== "pending" && (
          <div className={`reveal ${revealed ? "in" : ""}`} style={{ ["--tone" as string]: tone }}>
            <span className="verdict">{VERDICT[verdict].title}</span>
            {multiple !== null && <span className="mult">{multiple.toFixed(2)}×</span>}
            {watch.myPaidMinor > 0 && (
              <span className="paid">{money(watch.myPaidMinor)} back on {money(watch.myStakeMinor)}</span>
            )}
            <span className="detail">
              settled at {price(settled ?? 0)}
              {verdict === "hit" && " — inside your band"}
              {verdict === "miss" && " — outside your band"}
              {verdict === "refunded" && " — nobody held the settled band, every stake returned"}
              {verdict === "void" && " — no pool at freeze"}
            </span>
            <Link href="/" className="cta again">Back to the grid</Link>
          </div>
        )}
      </div>

      <footer className="status">
        <span>Pool <b>{money(watch.poolTotalMinor)}</b></span>
        <span>On your band <b>{money(watch.stakedMinor)}</b></span>
        <span>Your stake <b>{money(watch.myStakeMinor)}</b></span>
        <span>Fair <b>{watch.fair ? `${watch.fair.toFixed(2)}×` : "—"}</b></span>
        <span>Pool pays <b>{watch.pool ? `${watch.pool.toFixed(2)}×` : "—"}</b></span>
        <span style={{ marginLeft: "auto" }}>Market <b>{watch.market.id.slice(0, 8)}</b></span>
      </footer>
    </div>
  );
}
