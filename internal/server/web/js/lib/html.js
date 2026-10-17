// Preact + htm: components are written as tagged templates, no build step.
import { h, render, Fragment, createContext } from "preact";
import htm from "htm";

export const html = htm.bind(h);
export { render, Fragment, createContext };
export {
  useState, useEffect, useMemo, useRef, useCallback, useReducer, useContext, useLayoutEffect,
} from "preact/hooks";
