"use client";

import { useCallback, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import type { Account, Cell, Column, Snapshot } from "@/lib/types";
import { countdown, money, price } from "@/lib/types";

const ACCOUNT = "web";
const AMOUNTS = [5_000, 10_000, 25_000, 50_000];

// Each reading answers a different question: what would I win, what does the
// book pay, what does the model say, and where do those two disagree.
const VIEWS = [
  { id: "pool", label: "Odds", hint: "what the cell pays per dollar staked" },
  { id: "payout", label: "Payout", hint: "what your stake returns if it lands" },
  { id: "edge", label: "Edge", hint: "book against model, above 1.00 is underpriced" },
] as const;
type View = (typeof VIEWS)[number]["id"];

// Keyed by cell index, never by row: the band window re-centres as spot
// drifts, so a row number stops meaning the same band.
type Selection = { marketId: string; cell: number };

// The quote is captured when you click and does not move. What follows is the
// price you decided on; the live number is shown beside it when they diverge.
type Quote = { multiple: number; payout: number; at: number };

const TOLERANCES = [
  { id: "strict", label: "Exact", factor: 0.99 },
  { id: "normal", label: "5%", factor: 0.95 },
  { id: "loose", label: "20%", factor: 0.8 },
  { id: "any", label: "Any", factor: 0 },
] as const;
type Tolerance = (typeof TOLERANCES)[number]["id"];

// Reconnects with backoff and hands every frame to onSnapshot. The engine
// pushes whole snapshots, so a reconnect needs no replay to become correct.
function useSnapshotStream(wsURL: string) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [connected, setConnected] = useState(false);
  const [frames, setFrames] = useState(0);

  useEffect(() => {
    let socket: WebSocket | null = null;
    let retry: ReturnType<typeof setTimeout>;
    let backoff = 500;
    let closed = false;

    const open = () => {
      socket = new WebSocket(wsURL);
      socket.onopen = () => {
        setConnected(true);
        backoff = 500;
      };
      socket.onmessage = (event) => {
        setSnapshot(JSON.parse(event.data) as Snapshot);
        setFrames((n) => n + 1);
      };
      socket.onclose = () => {
        setConnected(false);
        if (closed) return;
        retry = setTimeout(open, backoff);
        backoff = Math.min(backoff * 2, 10_000);
      };
      socket.onerror = () => socket?.close();
    };

    open();
    return () => {
      closed = true;
      clearTimeout(retry);
      socket?.close();
    };
  }, [wsURL]);

  return { snapshot, connected, frames };
}

const tint = (p: number) => `rgba(115,145,182,${(Math.sqrt(p) * 0.5).toFixed(3)})`;

function multiplier(value: number | null) {
  if (value === null) return "—";
  if (value >= 10_000) return `${(value / 1000).toFixed(0)}k×`;
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k×`;
  return `${value.toFixed(2)}×`;
}

function compactMoney(minor: number) {
  const v = minor / 100;
  if (v >= 1_000_000) return `$${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 10_000) return `$${(v / 1000).toFixed(0)}k`;
  if (v >= 1000) return `$${(v / 1000).toFixed(1)}k`;
  return `$${v.toFixed(0)}`;
}

// The live multiple already includes your own stake, so what a cell would pay
// you is computed against the pool you would be joining, not the one you see.
type Reading = { primary: string; secondary: string; muted?: boolean };

function readCell(cell: Cell, column: Column, view: View, stake: number): Reading {
  const live = cell.pool ?? cell.fair;
  const chance = cell.probability >= 0.001 ? `${(cell.probability * 100).toFixed(1)}%` : "<0.1%";
  switch (view) {
    case "payout": {
      if (cell.fair === null) return { primary: "—", secondary: "" };
      const net = (column.poolMinor + stake) * (1 - column.rakeBps / 10000);
      const payout = (net * stake) / (cell.stakedMinor + stake);
      // With a thin pool every cell returns nearly the same, so the model's
      // probability is what actually separates them.
      return { primary: compactMoney(payout), secondary: `${chance} chance` };
    }
    case "edge": {
      if (cell.pool === null || cell.fair === null) {
        return { primary: multiplier(cell.fair), secondary: `${chance} chance`, muted: true };
      }
      const ratio = cell.pool / cell.fair;
      return { primary: ratio.toFixed(2), secondary: multiplier(cell.pool) };
    }
    default:
      return { primary: multiplier(live), secondary: `${chance} chance` };
  }
}

export default function LiveGrid({ httpBase, wsURL }: { httpBase: string; wsURL: string }) {
  const { snapshot, connected, frames } = useSnapshotStream(wsURL);
  const [selected, setSelected] = useState<Selection | null>(null);
  const [amount, setAmount] = useState(AMOUNTS[2]);
  const params = useSearchParams();
  const initialView = (VIEWS.find((v) => v.id === params.get("view"))?.id ?? "pool") as View;
  const [view, setView] = useState<View>(initialView);
  const [quote, setQuote] = useState<Quote | null>(null);
  const [tolerance, setTolerance] = useState<Tolerance>("normal");
  const [account, setAccount] = useState<Account | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const router = useRouter();

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, []);

  const loadAccount = useCallback(async () => {
    const res = await fetch(`${httpBase}/api/accounts/${ACCOUNT}`);
    if (res.ok) setAccount((await res.json()) as Account);
  }, [httpBase]);

  useEffect(() => {
    void loadAccount();
    const id = setInterval(() => void loadAccount(), 2000);
    return () => clearInterval(id);
  }, [loadAccount]);

  const stake = async () => {
    if (!selected || !quote) return;
    setError(null);

    const factor = TOLERANCES.find((t) => t.id === tolerance)!.factor;

    const res = await fetch(`${httpBase}/api/stakes`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        marketId: selected.marketId,
        account: ACCOUNT,
        cell: selected.cell,
        amountMinor: amount,
        minMultiple: quote.multiple * factor,
      }),
    });
    if (!res.ok) {
      const body = (await res.json()) as { error?: string };
      setError(body.error ?? `request failed with ${res.status}`);
      return;
    }
    const { stakeId } = (await res.json()) as { stakeId: string };
    router.push(`/watch/${selected.marketId}?cell=${selected.cell}&stake=${stakeId}`);
  };

  if (!snapshot) {
    return (
      <div style={{ padding: 40 }}>
        <span className="lbl">{connected ? "waiting for the first frame" : "connecting to the engine"}</span>
      </div>
    );
  }

  const { bands, columns } = snapshot;
  const activeView = VIEWS.find((v) => v.id === view)!;
  const thinPool = columns.every((c) => c.poolMinor < amount);
  const spotRow = bands.findIndex((b) => b.isSpot);
  const spotBand = spotRow >= 0 ? bands[spotRow] : null;
  const spotOffset =
    spotBand && spotBand.upper !== null
      ? (spotBand.upper - snapshot.spot) / (spotBand.upper - spotBand.lower)
      : 0.5;
  const chosen = selected ? columns.find((c) => c.marketId === selected.marketId) : undefined;
  const chosenRow = chosen
    ? chosen.cells.findIndex((c) => c.tradeable && c.index === selected!.cell)
    : -1;
  const chosenCell = chosenRow >= 0 ? chosen!.cells[chosenRow] : undefined;
  const stakeable =
    !!chosen && !!chosenCell && chosenCell.tradeable &&
    chosen.phase !== "frozen" && chosen.phase !== "settled";

  const liveQuote = chosen && chosenCell ? quoteFor(chosenCell, chosen, amount) : null;
  const drift = quote && liveQuote ? liveQuote.multiple / quote.multiple - 1 : 0;
  const moved = Math.abs(drift) >= 0.01;

  const select = (next: Selection, cell: Cell, column: Column) => {
    setSelected(next);
    setQuote(quoteFor(cell, column, amount));
    setError(null);
  };

  const setStakeAmount = (next: number) => {
    setAmount(next);
    if (chosen && chosenCell) setQuote(quoteFor(chosenCell, chosen, next));
  };

  return (
    <>
      <header className="top">
        <div className="topcell">
          <span className="mark">RTP&nbsp;GRID</span>
          <span className="lbl">Digital option book</span>
        </div>
        <div className="rule" />
        <div className="topcell">
          <span style={{ fontSize: 15, fontWeight: 600 }}>{snapshot.symbol}</span>
          <span className="lbl">{columns.length} expiries open</span>
        </div>
        <div className="topcell">
          <span className="spot">{price(snapshot.spot)}</span>
        </div>
        <div className="views">
          {VIEWS.map((v) => (
            <button key={v.id} className="viewbtn" data-on={v.id === view} onClick={() => setView(v.id)}>
              {v.label}
            </button>
          ))}
        </div>
        <span className="viewhint">
          {view === "payout" && thinPool
            ? "pool is nearly empty — every cell returns your stake less rake, so judge by chance"
            : view === "edge" && thinPool
              ? "no stakes yet — showing the model's own odds until a book forms"
              : activeView.hint}
        </span>
        <div style={{ flex: 1 }} />
        {account && (
          <>
            <div className="topcell" style={{ alignItems: "flex-end" }}>
              <span className="lbl">Available</span>
              <span style={{ fontSize: 15, fontWeight: 500 }}>{money(account.availableMinor)}</span>
            </div>
            <div className="topcell" style={{ alignItems: "flex-end" }}>
              <span className="lbl">Escrowed</span>
              <span style={{ fontSize: 15, fontWeight: 500, color: "var(--accent)" }}>
                {money(account.escrowedMinor)}
              </span>
            </div>
            <div className="rule" />
          </>
        )}
        <div className="topcell" style={{ alignItems: "flex-end" }}>
          <span className="chip" style={{ color: connected ? "var(--pos)" : "var(--neg)" }}>
            <i className="dot" />
            {connected ? "streaming" : "reconnecting"}
          </span>
          <span className="lbl">{frames} frames</span>
        </div>
      </header>

      <div style={{ position: "relative" }}>
        <div
          className="grid"
          style={{
            gridTemplateColumns: `170px repeat(${columns.length}, minmax(0, 1fr))`,
            gridTemplateRows: `58px repeat(${bands.length}, 52px)`,
          }}
        >
          <div className="chd corner">
            <span className="lbl">Price band</span>
            <span className="lbl" style={{ color: "var(--faint)" }}>expiry &rarr;</span>
          </div>
          {columns.map((c) => (
            <ColumnHead key={c.marketId} column={c} now={now} />
          ))}

          {bands.map((band, row) => (
            <BandRow
              key={band.lower}
              lower={band.lower}
              upper={band.upper}
              holdsSpot={band.isSpot}
              spot={snapshot.spot}
              columns={columns}
              row={row}
              selected={selected}
              onSelect={select}
              view={view}
              stake={amount}
            />
          ))}
        </div>

        {spotRow >= 0 && (
          <div
            className="spotrow"
            style={{ top: 58 + spotRow * 52 + spotOffset * 52 }}
          >
            <span className="spottag">{price(snapshot.spot)}</span>
          </div>
        )}
      </div>

      <div className="legend">
        <span>
          Each cell is one question: <b>will {snapshot.symbol} settle inside this price band
          when this column expires?</b>
        </span>
        <span>
          <i className="swatch" style={{ background: tint(0.35) }} />
          shading is the model&rsquo;s probability
        </span>
        <span>
          <i className="swatch" style={{ background: "var(--pos)", height: 3 }} />
          bar is the share of the pool on that cell
        </span>
      </div>

      <div className="ticket">
        <div className="tcell" style={{ width: 300 }}>
          <span className="lbl">Selected cell</span>
          {chosen && chosenCell ? (
            <>
              <span className="big">
                {price(bands[chosenRow].lower)} – {price(bands[chosenRow].upper ?? 0)}
              </span>
              <span className="lbl">
                expires in {countdown(chosen.expiresAt, now)} · {chosen.phase}
              </span>
            </>
          ) : (
            <span className="lbl" style={{ color: "var(--dim)" }}>Click any cell to load it here</span>
          )}
        </div>

        <div className="tcell" style={{ width: 290 }}>
          <span className="lbl">Stake</span>
          <div className="amts">
            {AMOUNTS.map((a) => (
              <button key={a} className="amt" data-on={a === amount} onClick={() => setStakeAmount(a)}>
                {money(a)}
              </button>
            ))}
          </div>
          <div className="amts">
            <span className="lbl" style={{ alignSelf: "center", marginRight: 4 }}>accept</span>
            {TOLERANCES.map((t) => (
              <button key={t.id} className="amt" data-on={t.id === tolerance} onClick={() => setTolerance(t.id)}>
                {t.label}
              </button>
            ))}
          </div>
        </div>

        {chosenCell && chosen && quote ? (
          <TicketNumbers
            cell={chosenCell}
            column={chosen}
            stake={amount}
            quote={quote}
            live={liveQuote}
            moved={moved}
            drift={drift}
          />
        ) : (
          <div className="tcell" style={{ flex: 1 }} />
        )}

        <div className="tcell" style={{ width: 250, borderRight: 0 }}>
          <span className="lbl">Commit</span>
          <button className="cta" data-moved={moved} onClick={stake} disabled={!stakeable}>
            {!chosen
              ? "Pick a cell"
              : chosen.phase === "settled"
                ? "Settled"
                : chosen.phase === "frozen"
                  ? "Frozen"
                  : moved
                  ? "Confirm at the new price"
                  : "Stake and watch"}
          </button>
          {error && <span className="lbl err">{error}</span>}
        </div>
      </div>

      <footer className="status">
        <span>
          Frame <b>{new Date(snapshot.at).toLocaleTimeString()}</b>
        </span>
        <span>
          Bands <b>{bands.length}</b>
        </span>
        <span>
          Positions <b>{account?.positions.length ?? 0}</b>
        </span>
        <span style={{ marginLeft: "auto" }}>
          Multipliers are indicative — the pool decides the payout
        </span>
      </footer>
    </>
  );
}

const PHASE_TONE: Record<string, string> = {
  amendable: "var(--pos)",
  committed: "var(--accent)",
  frozen: "var(--neg)",
  settled: "var(--dim)",
};

function ColumnHead({ column, now }: { column: Column; now: number }) {
  const done = column.phase === "settled";
  return (
    <div className="chd" data-settled={done}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
        <span className="cdn">
          {done && column.settledPrice !== null ? price(column.settledPrice) : countdown(column.expiresAt, now)}
        </span>
        <span className="chip" style={{ color: PHASE_TONE[column.phase] }}>
          <i className="dot" />
          {done ? column.outcome ?? "settled" : column.phase}
        </span>
      </div>
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <span className="lbl">{done ? "settled price" : `rake ${column.rakeBps} bps`}</span>
        <span style={{ fontSize: 11, color: "var(--dim)", fontWeight: 500 }}>{money(column.poolMinor)}</span>
      </div>
    </div>
  );
}

function BandRow({
  lower,
  upper,
  holdsSpot,
  spot,
  columns,
  row,
  selected,
  onSelect,
  view,
  stake,
}: {
  lower: number;
  upper: number | null;
  holdsSpot: boolean;
  spot?: number;
  columns: Column[];
  row: number;
  selected: Selection | null;
  onSelect: (s: Selection, cell: Cell, column: Column) => void;
  view: View;
  stake: number;
}) {
  return (
    <>
      <div className="pband" data-spot={holdsSpot}>
        <span className="pb1">{price(lower)} – {price(upper ?? lower)}</span>
        {holdsSpot && spot !== undefined && <span className="pb2">spot {price(spot)}</span>}
      </div>
      {columns.map((column) => {
        const cell = column.cells[row];
        const isSelected = selected?.marketId === column.marketId && selected.cell === cell.index;
        return (
          <CellView
            key={column.marketId}
            cell={cell}
            column={column}
            selected={isSelected}
            onSelect={() => onSelect({ marketId: column.marketId, cell: cell.index }, cell, column)}
            view={view}
            stake={stake}
          />
        );
      })}
    </>
  );
}

function CellView({
  cell,
  column,
  selected,
  onSelect,
  view,
  stake,
}: {
  cell: Cell;
  column: Column;
  selected: boolean;
  onSelect: () => void;
  view: View;
  stake: number;
}) {
  const dead = cell.probability < 0.0002 && cell.stakedMinor === 0;
  const edge = cell.pool !== null && cell.fair !== null ? (cell.pool > cell.fair ? "up" : "dn") : "";
  const share = column.poolMinor > 0 ? (cell.stakedMinor / column.poolMinor) * 100 : 0;
  if (!cell.tradeable) {
    return (
      <div className="cell" data-phase="outside">
        <span className="cm">·</span>
        <span className="cs none">outside this market</span>
      </div>
    );
  }

  const { primary, secondary, muted } = readCell(cell, column, view, stake);
  const reach = cell.probability >= 0.04 ? "near" : cell.probability >= 0.004 ? "mid" : "far";
  if (column.phase === "settled") {
    if (!cell.tradeable) return <div className="cell" data-phase="outside" />;
    return (
      <div className="cell" data-phase="settled" data-landed={cell.settled}>
        <span className="cm">
          {cell.settled ? "LANDED" : cell.stakedMinor ? money(cell.stakedMinor) : ""}
        </span>
        <span className="cs">
          {cell.settled
            ? cell.pool
              ? `paid ${multiplier(cell.pool)}`
              : "nobody held it"
            : cell.stakedMinor
              ? "lost"
              : ""}
        </span>
      </div>
    );
  }

  return (
    <div
      className="cell"
      data-selected={selected}
      data-dead={dead}
      data-landed={cell.settled}
      data-phase={column.phase}
      data-reach={reach}
      data-muted={muted ? "true" : undefined}
      data-edge={view === "edge" && !muted ? edge : ""}
      onClick={onSelect}
      style={{ ["--cellTint" as string]: tint(cell.probability) }}
    >
      <span className="cm">{dead ? "—" : primary}</span>
      <span className={`cs${secondary ? "" : " none"}`}>{dead ? "·" : secondary || "·"}</span>
      {share > 0 && <span className={`pbar ${edge}`} style={{ width: `${Math.min(share, 100)}%` }} />}
    </div>
  );
}

// Break-even is the hit rate a strategy needs to survive at this multiple: the
// single number that says whether a price is worth taking.
function TicketNumbers({
  cell,
  column,
  stake,
  quote,
  live,
  moved,
  drift,
}: {
  cell: Cell;
  column: Column;
  stake: number;
  quote: Quote;
  live: Quote | null;
  moved: boolean;
  drift: number;
}) {
  const payout = quote.payout;
  const takeMultiple = quote.multiple;
  const breakEven = takeMultiple > 0 ? 1 / takeMultiple : 0;
  const edge = cell.fair !== null && cell.fair > 0 ? takeMultiple / cell.fair : null;

  return (
    <div className="tcell numbers" style={{ flex: 1 }}>
      <div className="numgrid">
        <span className="lbl">You collect</span>
        <span className="lbl">At</span>
        <span className="lbl">Model p</span>
        <span className="lbl">Break-even</span>
        <span className="lbl">Against model</span>

        <span className="numbig" style={{ color: "var(--pos)" }}>{money(payout)}</span>
        <span className="num">{takeMultiple.toFixed(2)}×</span>
        <span className="num" style={{ color: "var(--model)" }}>{(cell.probability * 100).toFixed(2)}%</span>
        <span className="num">{(breakEven * 100).toFixed(1)}%</span>
        <span className="num" style={{ color: !edge ? "var(--dim)" : edge >= 1 ? "var(--pos)" : "var(--neg)" }}>
          {edge ? `${edge.toFixed(2)}×` : "—"}
        </span>
      </div>
      {moved && live ? (
        <span className="moved">
          price moved {drift > 0 ? "up" : "down"} to {live.multiple.toFixed(2)}× ({money(live.payout)})
          since you picked it — the button confirms at the new price
        </span>
      ) : (
        <span className="lbl" style={{ color: "var(--faint)" }}>
          quoted when you clicked, and held until you change the stake
        </span>
      )}
      <span className="lbl" style={{ color: "var(--faint)" }}>
        {edge === null
          ? "no model price for this cell, so there is nothing to compare against"
          : edge >= 1
            ? "the book pays above the model here — underpriced"
            : "the book pays below the model here — crowded"}
      </span>
    </div>
  );
}

function quoteFor(cell: Cell, column: Column, stake: number): Quote {
  const net = (column.poolMinor + stake) * (1 - column.rakeBps / 10000);
  const payout = (net * stake) / (cell.stakedMinor + stake);
  return { multiple: payout / stake, payout, at: Date.now() };
}
