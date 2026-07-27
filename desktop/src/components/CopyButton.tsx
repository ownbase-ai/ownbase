import { useEffect, useState } from "react";

import { Button } from "./ui";

/**
 * Copy-to-clipboard with confirmation.
 *
 * The public key is the one thing in setup the user has to move by hand into a
 * provider's web form, so "did that actually copy?" is a question worth
 * answering rather than leaving to faith.
 */
export function CopyButton({
  value,
  label = "Copy",
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1600);
    return () => clearTimeout(timer);
  }, [copied]);

  return (
    <Button
      variant="secondary"
      className={className}
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        setCopied(true);
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}
