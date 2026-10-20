import { Link } from "react-router";
import { EmptyState } from "../components/ui";

export function NotFound() {
  return (
    <EmptyState
      title="Page not found"
      action={
        <Link className="btn-primary" to="/devices">
          Go to devices
        </Link>
      }
    >
      The page you were looking for does not exist.
    </EmptyState>
  );
}
