// The result of a Test connection or Connect/Disconnect attempt, rendered
// inline under ProfileForm's action buttons. "success" mirrors
// service.ConnectionOK; every other ConnectionStatus, and a Connect failure,
// renders as "danger" with the backend's message shown verbatim.
export type ConnectionOutcome = { kind: "success" | "danger"; message: string };
