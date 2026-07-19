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
      <div>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      <Flex align="center" gap="3">
        {action}
        {status ? (
          <Badge color={ready ? "teal" : "gray"} variant="soft" radius="full">
            {status}
          </Badge>
        ) : null}
      </Flex>
    </header>
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
      {hint ? <p className="hint">{hint}</p> : null}
      {children}
    </div>
  );
}
