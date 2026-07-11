import { redirect } from "react-router";

// /mail → the inbox.
export const loader = () => redirect("/mail/INBOX");
