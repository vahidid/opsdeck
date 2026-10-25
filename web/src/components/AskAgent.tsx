import { useNavigate } from "react-router-dom";
import { Bot } from "lucide-react";
import { useWs } from "../context/app";
import { Button } from "./ui";

// AskAgentButton hands the current screen's question to the agent: a new
// session opens with the prompt as its draft (sent only when you press Enter).
export function AskAgentButton({
  prompt,
  label = "Ask the agent",
  size = "md",
  solid,
  className,
}: {
  prompt: string;
  label?: string;
  size?: "xs" | "sm" | "md";
  solid?: boolean;
  className?: string;
}) {
  const { name } = useWs();
  const navigate = useNavigate();
  return (
    <Button
      size={size}
      variant={solid ? "agent" : "agentSoft"}
      icon={<Bot className={size === "md" ? "size-4" : "size-3.5"} />}
      className={className}
      title="Opens a new agent session with this question as a draft"
      onClick={() => navigate(`/w/${encodeURIComponent(name)}/agent?new=1&draft=${encodeURIComponent(prompt)}`)}
    >
      {label}
    </Button>
  );
}
