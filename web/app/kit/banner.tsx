import type { ReactNode } from "react";
import { AlertIcon, InfoIcon, SuccessIcon, WarnIcon } from "./icon";

const toneMap = {
  info: { box: "border-line bg-raised text-ink-2", Icon: InfoIcon },
  ok: { box: "border-ok/30 bg-ok-soft text-ok", Icon: SuccessIcon },
  warn: { box: "border-warn/30 bg-warn-soft text-warn", Icon: WarnIcon },
  bad: { box: "border-bad/30 bg-bad-soft text-bad", Icon: AlertIcon },
} as const;

export type BannerTone = keyof typeof toneMap;

export const Banner = ({
  tone = "info",
  children,
  action,
}: {
  tone?: BannerTone;
  children: ReactNode;
  action?: ReactNode;
}) => {
  const { box, Icon } = toneMap[tone];
  return (
    <div className={`flex items-start gap-2.5 rounded-md border px-3 py-2.5 text-sm ${box}`}>
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden />
      <div className="min-w-0 flex-1">{children}</div>
      {action}
    </div>
  );
};

/** Renders nothing when there is no message — keeps routes free of ternaries. */
export const ErrorBanner = ({ message }: { message?: string | null }) =>
  message ? <Banner tone="bad">{message}</Banner> : null;
