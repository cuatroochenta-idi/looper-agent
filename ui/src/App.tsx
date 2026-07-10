import { type JSX } from "solid-js";
import { Router, Route } from "@solidjs/router";
import { BASE } from "./lib/base";
import { AppShell } from "./AppShell";
import { Dashboard } from "./routes/Dashboard";
import { Runs } from "./routes/Runs";
import { Chats } from "./routes/Chats";
import { Login } from "./routes/Login";

function Shell(props: { children?: JSX.Element }) {
  return <AppShell>{props.children}</AppShell>;
}

export function App() {
  return (
    <Router base={BASE}>
      <Route path="/login" component={Login} />
      <Route path="/" component={Shell}>
        <Route path="/" component={Dashboard} />
        <Route path="/chats/:key?" component={Chats} />
        <Route path="/runs/:id?" component={Runs} />
      </Route>
    </Router>
  );
}
