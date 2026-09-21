import { useState } from "react";
import { CheckIcon, MinusIcon, XIcon } from "./icon";

// Three-state permission switch: deny / inherit / allow, left to right.
//
// The shape is the one people already know from Discord's role editor — a
// single control with the neutral choice in the middle, so "I said nothing
// here" reads differently from "I said no". A row of selects made every rule
// look like a decision even when it was silence.
//
// It keeps its own state and writes to a hidden input, so it drops into a
// plain form post without any wiring.

export type Grant = "allow" | "deny" | "inherit";

const toneMap = {
  deny: "bg-bad-soft text-bad",
  inherit: "bg-raised text-ink-2",
  allow: "bg-ok-soft text-ok",
} as const;

export const GrantSwitch = ({
  name,
  value,
  onChange,
  allowInherit = true,
  disabled,
  label,
}: {
  name?: string;
  value: Grant;
  onChange?: (next: Grant) => void;
  /** The default group has nothing to inherit from, so it only says yes or no. */
  allowInherit?: boolean;
  disabled?: boolean;
  label?: string;
}) => {
  const [current, setCurrent] = useState<Grant>(value);
  const choiceList: Grant[] = allowInherit ? ["deny", "inherit", "allow"] : ["deny", "allow"];

  const pick = (next: Grant) => {
    setCurrent(next);
    onChange?.(next);
  };

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex shrink-0 overflow-hidden rounded-md border border-line bg-canvas"
    >
      {choiceList.map((choice) => {
        const active = current === choice;
        const Icon = choice === "allow" ? CheckIcon : choice === "deny" ? XIcon : MinusIcon;
        return (
          <button
            key={choice}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={choice}
            disabled={disabled}
            onClick={() => pick(choice)}
            className={`flex h-7 w-9 items-center justify-center border-line transition-colors duration-100 not-first:border-l ${
              active ? toneMap[choice] : "text-ink-faint hover:bg-raised/60 hover:text-ink-3"
            } ${disabled ? "cursor-not-allowed opacity-50" : ""}`}
          >
            <Icon className="size-3.5" strokeWidth={active ? 2.5 : 2} />
          </button>
        );
      })}
      {name && <input type="hidden" name={name} value={current} />}
    </div>
  );
};
