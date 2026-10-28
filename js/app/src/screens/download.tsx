import { Text, useTheme, View } from "@streamplace/components";
import { LogoMark } from "components/brand/logo";
import { ScrollView } from "react-native";
import GetApps from "../../components/get-apps";

function SectionLabel({ children }: { children: string }) {
  const { theme } = useTheme();
  return (
    <Text
      weight="semibold"
      style={{
        fontSize: 11,
        letterSpacing: 1.4,
        textTransform: "uppercase",
        color: theme.colors.text3,
      }}
    >
      {children}
    </Text>
  );
}

export default function DownloadScreen() {
  const { theme } = useTheme();
  const c = theme.colors;

  const card = {
    width: "100%" as const,
    padding: 28,
    borderRadius: 16,
    backgroundColor: c.surface1,
    borderWidth: 1,
    borderColor: c.borderSubtle,
    alignItems: "center" as const,
    gap: 18,
  };

  return (
    <ScrollView
      contentContainerStyle={{
        alignItems: "center",
        paddingHorizontal: 24,
        paddingVertical: 56,
      }}
    >
      <View
        style={{ width: "100%", maxWidth: 540, gap: 32, alignItems: "center" }}
      >
        {/* Hero */}
        <View style={{ alignItems: "center", gap: 16 }}>
          <LogoMark size={48} color={c.text1} />
          <View style={{ alignItems: "center", gap: 10 }}>
            <Text
              weight="semibold"
              style={{
                color: c.text1,
                fontSize: 30,
                letterSpacing: -0.4,
                textAlign: "center",
              }}
            >
              Get Streamplace
            </Text>
            <Text
              style={{
                color: c.text2,
                fontSize: 16,
                lineHeight: 24,
                textAlign: "center",
                maxWidth: 420,
              }}
            >
              Watch, stream, and manage your channel on mobile or the web.
            </Text>
          </View>
        </View>

        {/* Mobile */}
        <View style={card}>
          <SectionLabel>Mobile</SectionLabel>
          <GetApps />
        </View>
      </View>
    </ScrollView>
  );
}
