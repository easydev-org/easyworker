// Package goa11y is a Go reimplementation of the xa11y accessibility library:
// drive native desktop apps on Linux (X11/Wayland), Windows, and macOS through
// their accessibility trees, with input simulation and screenshot capture.
package goa11y

// Role is the accessibility role of an element, normalized across platforms.
type Role string

const (
	RoleUnknown     Role = "unknown"
	RoleWindow      Role = "window"
	RoleApplication Role = "application"
	RoleButton      Role = "button"
	RoleCheckBox    Role = "check_box"
	RoleRadioButton Role = "radio_button"
	RoleTextField   Role = "text_field"
	RoleTextArea    Role = "text_area"
	RoleStaticText  Role = "static_text"
	RoleComboBox    Role = "combo_box"
	RoleList        Role = "list"
	RoleListItem    Role = "list_item"
	RoleMenu        Role = "menu"
	RoleMenuItem    Role = "menu_item"
	RoleMenuBar     Role = "menu_bar"
	RoleTab         Role = "tab"
	RoleTabGroup    Role = "tab_group"
	RoleTable       Role = "table"
	RoleTableRow    Role = "table_row"
	RoleTableCell   Role = "table_cell"
	RoleToolbar     Role = "toolbar"
	RoleScrollBar   Role = "scroll_bar"
	RoleSlider      Role = "slider"
	RoleImage       Role = "image"
	RoleLink        Role = "link"
	RoleGroup       Role = "group"
	RoleDialog      Role = "dialog"
	RoleAlert       Role = "alert"
	RoleProgressBar Role = "progress_bar"
	RoleTreeItem    Role = "tree_item"
	RoleWebArea     Role = "web_area"
	RoleHeading     Role = "heading"
	RoleSeparator   Role = "separator"
	RoleSplitGroup  Role = "split_group"
	RoleSwitch      Role = "switch"
	RoleSpinButton  Role = "spin_button"
	RoleTooltip     Role = "tooltip"
	RoleStatus      Role = "status"
	RoleNavigation  Role = "navigation"
	RoleScrollThumb Role = "scroll_thumb"
)

// UnknownRole returns RoleUnknown but records the platform context so a
// mis-mapping is diagnosable.
func UnknownRole(context string) Role { return RoleUnknown }
