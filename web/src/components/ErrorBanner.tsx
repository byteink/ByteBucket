// Centralizes the failure treatment so every surface gets the same red danger
// signal and role="alert" semantics. The monochrome ink ramp otherwise lets
// errors read as ordinary body copy.
export function ErrorBanner({ message, className = '' }: { message: string; className?: string }) {
  return (
    <div
      role="alert"
      className={`text-xs text-danger border-l-2 border-danger pl-3 whitespace-pre-wrap ${className}`}
    >
      {message}
    </div>
  );
}
