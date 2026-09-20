// Deterministic initial avatar — same address always gets the same hue.
const hueOf = (seed: string): number => {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) % 360;
  return h;
};

const initialOf = (label: string): string => {
  const trimmed = label.trim();
  if (!trimmed) return "?";
  const first = trimmed[0] ?? "?";
  return first.toUpperCase();
};

export const Avatar = ({
  label,
  seed,
  size = 32,
}: {
  label: string;
  /** Colour source — defaults to the label (use the address for stability). */
  seed?: string;
  size?: number;
}) => {
  const hue = hueOf(seed ?? label);
  return (
    <span
      aria-hidden
      style={{
        width: size,
        height: size,
        background: `oklch(0.62 0.11 ${hue} / 0.22)`,
        color: `oklch(0.78 0.12 ${hue})`,
        fontSize: Math.round(size * 0.42),
      }}
      className="inline-flex shrink-0 items-center justify-center rounded-full font-semibold"
    >
      {initialOf(label)}
    </span>
  );
};
