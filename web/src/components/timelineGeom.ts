// Timeline canvas geometry, shared by the canvas and the dock that sizes it.
export const AXIS = 20 // time labels at the bottom
export const MINI = 24 // overview strip at the top
export const LGAP = 3 // between lanes
export const LANE_H = 24 // default lane height; the user can drag the timeline taller or shorter
export const MIN_LANE = 14
export const MAX_LANE = 200

/** Canvas height for this many camera lanes of laneH pixels each. */
export const timelineHeight = (cameras: number, laneH = LANE_H) => MINI + 4 + cameras * laneH + Math.max(0, cameras - 1) * LGAP + AXIS
