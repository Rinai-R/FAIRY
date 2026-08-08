import { Badge, Flex, Text } from "@radix-ui/themes";
import type { ReactNode } from "react";

export function PageHeader({
  title,
  description,
  status,
  ready = false,
  action,
}: {
  title: string;
  description: string;
  status?: string;
  ready?: boolean;
  action?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div className="page-heading-copy">
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      <Flex align="center" gap="3" className="page-header-actions">
        {action}
        {status ? (
          <Badge className="page-status" color={ready ? "green" : "gray"} variant="soft" radius="small">
            {status}
          </Badge>
        ) : null}
      </Flex>
    </header>
  );
}

export function ConfigSection({
  title,
  description,
  children,
  className = "",
}: {
  title: string;
  description?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`config-section ${className}`.trim()}>
      <header className="config-section-heading">
        <h2>{title}</h2>
        {description ? <p>{description}</p> : null}
      </header>
      <div className="config-section-body">{children}</div>
    </section>
  );
}

export function SectionHeading({
  title,
  description,
  aside,
}: {
  title: string;
  description?: string;
  aside?: ReactNode;
}) {
  return (
    <div className="section-heading">
      <div>
        <h2>{title}</h2>
        {description ? <p>{description}</p> : null}
      </div>
      {aside ? <div className="section-heading-aside">{aside}</div> : null}
    </div>
  );
}

export function EmptyState({ title, description }: { title: string; description?: string }) {
  return (
    <div className="empty-state">
      <strong>{title}</strong>
      {description ? <p>{description}</p> : null}
    </div>
  );
}

export function InlineNotice({
  tone = "neutral",
  title,
  children,
}: {
  tone?: "neutral" | "warning" | "error";
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className={`inline-notice ${tone}`} role={tone === "error" ? "alert" : undefined}>
      {title ? <strong>{title}</strong> : null}
      <span>{children}</span>
    </div>
  );
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <Text as="label" size="2" weight="medium">
        {label}
      </Text>
      <p className={`hint ${hint ? "" : "empty"}`.trim()} aria-hidden={!hint}>
        {hint || "\u00a0"}
      </p>
      {children}
    </div>
  );
}
