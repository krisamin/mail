/** Dashboard stat card. */
export const StatCard = ({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: string;
}) => (
  <div className="rounded-md border border-line bg-bg-1 p-4">
    <p className="text-xs text-text-2">{label}</p>
    <p className={`mt-1 text-2xl font-bold ${tone ?? "text-text-0"}`}>{value}</p>
  </div>
);
