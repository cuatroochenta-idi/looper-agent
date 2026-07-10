import { createResource, createRoot } from "solid-js";
import { api } from "../api";
import { range, sinceParam } from "./timeRange";
import { debounce, sseHub } from "./sseHub";

function createChatsStore() {
  const [chats, { refetch }] = createResource(() => sinceParam(range()), api.getChats);
  sseHub.onChatsChanged(debounce(() => void refetch(), 250));
  return { chats, refetch };
}

export const chatsStore = createRoot(createChatsStore);
