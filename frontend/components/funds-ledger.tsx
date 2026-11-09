"use client";

import { useEffect, useState } from "react";
import { ACCOUNT, getJSON } from "@/lib/api";
import { money, type Account } from "@/lib/types";

interface Entry {
  kind: string;
  amountMinor: number;
  marketId: string;
  at: string;
}

const TONE: Record<string, string> = {
  deposit: "var(--pos)",
  payout: "var(--pos)",
  refund: "var(--accent)",
  cancel: "var(--accent)",
  stake: "var(--dim)",
};

export default function FundsLedger() {
  const [account, setAccount] = useState<Account | null>(null);
  const [entries, setEntries] = useState<Entry[]>([]);

  useEffect(() => {
    const load = async () => {
      setAccount(await getJSON<Account>(`/api/accounts/${ACCOUNT}`));
      setEntries(await getJSON<Entry[]>(`/api/accounts/${ACCOUNT}/ledger`));
    };
    void load();
    const id = setInterval(load, 1500);
    return () => clearInterval(id);
  }, []);

  if (!account) return <div style={{ padding: 30 }}><span className="lbl">loading</span></div>;

  return (
    <>
      <header className="top">
        <div className="topcell">
          <span className="lbl">Funds</span>
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
        <div style={{ flex: 1 }} />
        <div className="topcell" style={{ alignItems: "flex-end" }}>
          <span className="lbl">Play money — no wallet connected yet</span>
        </div>
      </header>

      <div className="sect"><span className="lbl">Ledger</span><span className="lbl">{entries.length} entries</span></div>
      <div className="rows">
        {entries.map((e, i) => (
          <div className="prow" key={`${e.at}-${i}`} style={{ gridTemplateColumns: "110px 90px 1fr 130px" }}>
            <span className="lbl" style={{ color: TONE[e.kind] ?? "var(--dim)" }}>{e.kind}</span>
            <span style={{ color: "var(--dim)" }}>{new Date(e.at).toLocaleTimeString()}</span>
            <span style={{ color: "var(--faint)" }}>{e.marketId ? e.marketId.slice(0, 8) : "—"}</span>
            <span className="r" style={{ color: e.amountMinor > 0 ? "var(--pos)" : "var(--text)" }}>
              {e.amountMinor > 0 ? "+" : "−"}{money(Math.abs(e.amountMinor)).slice(1)}
            </span>
          </div>
        ))}
      </div>
    </>
  );
}
