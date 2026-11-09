export type Phase = "amendable" | "committed" | "frozen" | "settled";

export interface Band {
  lower: number;
  upper: number | null;
  isSpot: boolean;
}

export interface Cell {
  index: number;
  tradeable: boolean;
  settled: boolean;
  probability: number;
  fair: number | null;
  stakedMinor: number;
  pool: number | null;
}

export interface Column {
  marketId: string;
  phase: Phase;
  outcome: string | null;
  settledPrice: number | null;
  settledCell: number | null;
  amendsAt: string;
  freezesAt: string;
  expiresAt: string;
  rakeBps: number;
  poolMinor: number;
  cells: Cell[];
}

export interface Snapshot {
  symbol: string;
  spot: number;
  at: string;
  bands: Band[];
  columns: Column[];
}

export interface Account {
  id: string;
  handle: string;
  availableMinor: number;
  escrowedMinor: number;
  positions: Position[];
}

export interface Position {
  stakeId: string;
  marketId: string;
  cellIndex: number;
  amountMinor: number;
  placedAt: string;
  cancelled: boolean;
  marketState: string;
  paidMinor: number;
  lower: number | null;
  upper: number | null;
  settledCell: number | null;
  amendsAt: string;
  expiresAt: string;
}

export const money = (minor: number) =>
  `$${(minor / 100).toLocaleString(undefined, { minimumFractionDigits: 2 })}`;

export const price = (value: number) =>
  value.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });

export const countdown = (iso: string, now: number) => {
  const ms = new Date(iso).getTime() - now;
  if (ms <= 0) return "0:00";
  const total = Math.floor(ms / 1000);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
};

export interface PricePoint {
  at: string;
  price: number;
}

export interface MarketDetail {
  id: string;
  symbol: string;
  state: string;
  cellWidth: number;
  rakeBps: number;
  opensAt: string;
  amendsAt: string;
  freezesAt: string;
  expiresAt: string;
  settledPrice: number | null;
  settledCell: number | null;
  outcome: string | null;
}

export interface Watch {
  market: MarketDetail;
  path: PricePoint[];
  spot: number;
  lower: number | null;
  upper: number | null;
  cell: number;
  fair: number | null;
  pool: number | null;
  stakedMinor: number;
  poolTotalMinor: number;
  myStakeMinor: number;
  myPaidMinor: number;
  resolved: boolean;
  hit: boolean | null;
  stakeId: string;
}

export type Verdict = "hit" | "miss" | "refunded" | "void" | "pending";

export function verdictOf(w: Watch): Verdict {
  if (!w.resolved && w.market.state !== "settled" && w.market.state !== "voided") return "pending";
  if (w.market.outcome === "void") return "void";
  if (w.hit) return "hit";
  if (w.market.outcome === "refunded") return "refunded";
  return "miss";
}

export interface Summary {
  positions: number;
  stakedMinor: number;
  returnedMinor: number;
  netMinor: number;
  hits: number;
  refunds: number;
  misses: number;
  hitRate: number;
  expectancyPerDollar: number;
}

export interface Bucket {
  lower: number;
  upper: number;
  cells: number;
  predicted: number;
  hits: number;
  actual: number;
}

export interface Performance {
  summary: Summary;
  calibration: Bucket[];
  marketsAnalysed: number;
  sigma: number;
  generatedAt: string;
}
