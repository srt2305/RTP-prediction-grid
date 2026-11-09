"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const ITEMS = [
  { href: "/", label: "Grid", icon: "M3 3h18v18H3zM9 3v18M15 3v18M3 9h18M3 15h18" },
  { href: "/positions", label: "Pos", icon: "M12 3 3 8l9 5 9-5-9-5zM3 13l9 5 9-5" },
  { href: "/stats", label: "Stats", icon: "M4 19V9M9.3 19V5M14.7 19v-8M20 19v-5" },
  { href: "/funds", label: "Funds", icon: "M3 6h18v13H3zM3 10.5h18M17 15h.01" },
];

export default function Nav() {
  const path = usePathname();
  return (
    <nav className="rail">
      <div className="railmark">RTP</div>
      {ITEMS.map((item) => {
        const active = item.href === "/" ? path === "/" || path.startsWith("/watch") : path.startsWith(item.href);
        return (
          <Link key={item.href} href={item.href} className="nv" data-on={active}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.4"
                 strokeLinecap="round" strokeLinejoin="round">
              <path d={item.icon} />
            </svg>
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
