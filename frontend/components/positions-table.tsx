"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ACCOUNT, getJSON, HTTP } from "@/lib/api";
import { money, price, type Account, type Position } from "@/lib/types";

const STATE_TONE: Record<string, string> = {
  open: "var(--pos)",
  frozen: "var(--accent)",
  settled: "var(--dim)",
  voided: "var(--faint)",
};

export default function PositionsTable() {
  const [account, setAccount] = useState<Account | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = async () => {
    try {
      setAccount(await getJSON<Account>(`/api/accounts/${ACCOUNT}`));
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    void load();
    const id = setInterval(load, 1000);
    return () => clearInterval(id);
  }, []);

  const cancel = async (p: Position) => {
    const res = await fetch(`${HTTP}/api/stakes/${p.stakeId}?marketId=${p.marketId}`, { method: "DELETE" });
    if (!res.ok) {
      const body = (await res.json()) as { error?: string };
      setError(body.error ?? `cancel failed with ${res.status}`);
      return;
    }
    await load();
  };

  if (error) return <div style={{ padding: 30 }}><span className="lbl err">{error}</span></div>;
  if (!account) return <div style={{ padding: 30 }}><span className="lbl">loading positions</span></div>;

  const open = account.positions.filter((p) => !p.cancelled && (p.marketState === "open" || p.marketState === "frozen"));
  const done = account.positions.filter((p) => p.cancelled || (p.marketState !== "open" && p.marketState !== "frozen"));

  const resolved = done.filter((p) => !p.cancelled);
  const staked = resolved.reduce((n, p) => n + p.amountMinor, 0);
  const returned = resolved.reduce((n, p) => n + p.paidMinor, 0);
  const hits = resolved.filter((p) => p.settledCell === p.cellIndex).length;
  const refunded = resolved.filter((p) => p.settledCell !== p.cellIndex && p.paidMinor > 0).length;
  const misses = resolved.length - hits - refunded;
  const atRisk = open.reduce((n, p) => n + p.amountMinor, 0);

  return (
    <>
      <header className="top">
        <div className="topcell">
          <span className="lbl">Positions</span>
          <span style={{ fontSize: 15, fontWeight: 600 }}>{account.handle}</span>
        </div>
        <div className="rule" />
        <div className="topcell">
          <span className="lbl">Available</span>
          <span className="spot" style={{ fontSize: 21 }}>{money(account.availableMinor)}</span>
        </div>
        <div className="topcell">
          <span className="lbl">Escrowed</span>
          <span className="spot" style={{ fontSize: 21, color: "var(--accent)" }}>{money(account.escrowedMinor)}</span>
        </div>
        <div className="topcell">
          <span className="lbl">At risk</span>
          <span className="spot" style={{ fontSize: 21 }}>{money(atRisk)}</span>
        </div>
        <div className="rule" />
        <div className="topcell">
          <span className="lbl">Resolved net</span>
          <span className="spot" style={{ fontSize: 21,
                color: returned - staked >= 0 ? "var(--pos)" : "var(--neg)" }}>
            {money(returned - staked)}
          </span>
        </div>
        <div className="topcell">
          <span className="lbl">Hit rate</span>
          <span className="spot" style={{ fontSize: 21 }}>
            {hits + misses > 0 ? `${((hits / (hits + misses)) * 100).toFixed(0)}%` : "—"}
          </span>
        </div>
        <div style={{ flex: 1 }} />
        <div className="topcell" style={{ alignItems: "flex-end" }}>
          <span className="lbl">{open.length} open · {resolved.length} resolved</span>
          <span className="lbl">{hits} hit · {refunded} refunded · {misses} missed</span>
        </div>
      </header>

      <Section title="Open" empty="Nothing open. Pick a cell on the grid.">
        {open.map((p) => (
          <Row key={p.stakeId} p={p} onCancel={() => cancel(p)} />
        ))}
      </Section>

      <Section title="Resolved" empty="Nothing has settled yet.">
        {done.map((p) => (
          <Row key={p.stakeId} p={p} />
        ))}
      </Section>
    </>
  );
}

function Section({ title, empty, children }: { title: string; empty: string; children: React.ReactNode[] }) {
  return (
    <>
      <div className="sect"><span className="lbl">{title}</span><span className="lbl">{children.length}</span></div>
      {children.length === 0 ? (
        <div style={{ padding: "22px 22px 26px" }}><span className="lbl" style={{ color: "var(--dim)" }}>{empty}</span></div>
      ) : (
        <div className="rows">
          <div className="prow head">
            <span className="lbl">State</span>
            <span className="lbl">Cell</span>
            <span className="lbl">Price band</span>
            <span className="lbl r">Staked</span>
            <span className="lbl r">Returned</span>
            <span className="lbl r">Net</span>
            <span className="lbl r">Actions</span>
          </div>
          {children}
        </div>
      )}
    </>
  );
}

function Row({ p, onCancel }: { p: Position; onCancel?: () => void }) {
  const net = p.paidMinor - p.amountMinor;
  const resolved = p.marketState === "settled" || p.marketState === "voided";
  const hit = resolved && !p.cancelled && p.settledCell === p.cellIndex;
  const cancellable = !p.cancelled && Date.now() < new Date(p.amendsAt).getTime();
  return (
    <div className="prow">
      <span className="lbl" style={{ color: hit ? "var(--pos)" : STATE_TONE[p.marketState] ?? "var(--dim)" }}>
        {p.cancelled ? "cancelled" : hit ? "hit" : p.marketState}
      </span>
      <span style={{ fontWeight: 500 }}>{p.cellIndex}</span>
      <span style={{ color: "var(--dim)" }}>
        {p.lower === null || p.upper === null
          ? `tail cell ${p.cellIndex}`
          : `${price(p.lower)} – ${price(p.upper)}`}
        {resolved && !p.cancelled && p.settledCell !== null && (
          <span style={{ color: "var(--faint)", marginLeft: 12 }}>
            settled cell {p.settledCell}
          </span>
        )}
      </span>
      <span className="r">{money(p.amountMinor)}</span>
      <span className="r">{resolved && !p.cancelled ? money(p.paidMinor) : "—"}</span>
      <span className="r" style={{ color: net > 0 ? "var(--pos)" : net < 0 ? "var(--neg)" : "var(--dim)" }}>
        {p.cancelled ? "returned" : resolved ? `${net >= 0 ? "+" : "−"}${Math.abs(net / 100).toFixed(2)}` : "—"}
      </span>
      <span className="r">
        {!p.cancelled && (
          <Link className="link" href={`/watch/${p.marketId}?cell=${p.cellIndex}&stake=${p.stakeId}`}>
            watch
          </Link>
        )}
        {onCancel && cancellable && (
          <button className="amt" style={{ marginLeft: 8 }} onClick={onCancel}>
            cancel
          </button>
        )}
        {onCancel && !cancellable && <span className="lbl">committed</span>}
      </span>
    </div>
  );
}
