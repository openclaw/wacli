package store

// notReaction leaves out reaction rows, which belong to the message they react
// to and are no message of the chat's own.
const notReaction = "COALESCE(reaction_to_id,'') = '' AND COALESCE(reaction_emoji,'') = ''"

// notPlaceholder leaves out the content-free "(message)" rows WhatsApp system
// events are stored as; they move no chat activity and count as nobody's read.
const notPlaceholder = "(COALESCE(display_text,'') != '(message)' OR TRIM(COALESCE(text,'')) != '')"
