import { MailIcon } from "~/kit";

/** App mark — one glyph, used by the rail, the login screen and the tab icon. */
export const BrandMark = ({ size = 28 }: { size?: number }) => (
  <span
    style={{ width: size, height: size }}
    className="inline-flex items-center justify-center rounded-lg bg-brand text-brand-ink"
  >
    <MailIcon style={{ width: size * 0.55, height: size * 0.55 }} strokeWidth={2} />
  </span>
);
