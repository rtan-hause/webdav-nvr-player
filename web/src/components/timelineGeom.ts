// Timeline canvas geometry, shared by the canvas and the dock that sizes it.
export const AXIS = 20 // time labels at the bottom
export const MINI = 14 // overview strip at the top
export const LGAP = 3 // between lanes
export const LANE_H = 24

export const timelineHeight = (cameras: number) => MINI + 4 + cameras * LANE_H + Math.max(0, cameras - 1) * LGAP + AXIS
