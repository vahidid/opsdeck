// Marks of the Git hosting services (simple-icons, CC0).

export function GitHubMark({ className = "size-4" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden>
      <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
    </svg>
  );
}

export function GitLabMark({ className = "size-4" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden>
      <path d="m23.6 9.593-.033-.086L20.3.98a.851.851 0 0 0-.336-.405.875.875 0 0 0-1 .054.875.875 0 0 0-.29.44L16.47 7.818H7.537L5.333 1.07a.857.857 0 0 0-.29-.441.875.875 0 0 0-1-.054.859.859 0 0 0-.336.405L.433 9.502l-.032.086a6.066 6.066 0 0 0 2.012 7.01l.01.009.03.021 4.977 3.727 2.462 1.863 1.5 1.132a1.008 1.008 0 0 0 1.22 0l1.499-1.132 2.461-1.863 5.006-3.75.013-.01a6.068 6.068 0 0 0 2.01-7.002z" />
    </svg>
  );
}

// The DNS / CDN providers get plain marks in their colours (a cloud with a
// flare for Cloudflare, a cloud over a base line for ArvanCloud), not their
// trademarked logos.
export function CloudflareMark({ className = "size-4" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden style={{ color: "#f38020" }}>
      <path d="M17.5 18.5H7a4 4 0 0 1-.6-7.95A6 6 0 0 1 17.8 9.6" />
      <path d="M17.5 18.5a3.5 3.5 0 1 0-.9-6.9" />
      <path d="M3.5 21.5h17" opacity="0.55" />
    </svg>
  );
}

export function ArvanCloudMark({ className = "size-4" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden style={{ color: "#00a9a5" }}>
      <path d="M7 17.5a4 4 0 0 1-.4-7.98A6 6 0 0 1 18 11.5a3 3 0 0 1-.5 6H7Z" />
      <path d="M9.5 21.5h5" />
      <path d="M12 13.5v-3" opacity="0.7" />
    </svg>
  );
}

export function ProviderMark({ provider, className }: { provider: string; className?: string }) {
  switch (provider) {
    case "github":
      return <GitHubMark className={className} />;
    case "cloudflare":
      return <CloudflareMark className={className} />;
    case "arvancloud":
      return <ArvanCloudMark className={className} />;
  }
  return <GitLabMark className={className} />;
}
